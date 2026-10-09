// Package httpapi is the HTTP transport layer: routing, middleware, and handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/ryankupk/notes/internal/auth"
	"github.com/ryankupk/notes/internal/events"
	"github.com/ryankupk/notes/internal/note"
)

// Pinger reports whether a backing dependency is reachable. It is declared here,
// where it is consumed, so health checks can be tested without MongoDB.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators the router needs. Hub is optional: without it
// the WebSocket route is not registered.
type Deps struct {
	Pinger Pinger
	Notes  *note.Service
	Hub    *events.Hub
	Logger *slog.Logger

	// Verifier authenticates every /api/v1 route except the dev-token endpoint.
	// Nil rejects all requests.
	Verifier auth.Verifier
	// DevTokens, when set, registers POST /api/v1/auth/dev-token. Leave nil in
	// jwt mode so the route does not exist.
	DevTokens TokenIssuer
	// AllowedOrigins are the browser origins permitted for CORS and WebSocket
	// upgrades, e.g. "http://localhost:5173". Empty means same-origin only.
	AllowedOrigins []string
}

const (
	bodyLimit     = "1M"
	healthTimeout = 2 * time.Second
	gzipMinBytes  = 1024
)

// NewRouter builds the Echo instance with shared middleware and routes.
func NewRouter(deps Deps) *echo.Echo {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = newErrorHandler(logger)

	// Order matters: request ID first so recover and the logger can see it.
	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(requestLogger(logger))
	// CORS must come before authentication so a preflight OPTIONS, which
	// carries no credentials, is answered instead of rejected with 401.
	if len(deps.AllowedOrigins) > 0 {
		e.Use(corsMiddleware(deps.AllowedOrigins))
	}
	e.Use(middleware.BodyLimit(bodyLimit))
	// Note bodies are Markdown, which compresses several-fold; this mostly pays
	// off when viewers refetch a large note after every live update. Small
	// responses aren't worth the CPU, and the WebSocket upgrade is left alone.
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		MinLength: gzipMinBytes,
		Skipper:   func(c echo.Context) bool { return c.Path() == "/api/v1/ws" },
	}))

	h := &handler{
		pinger: deps.Pinger, notes: deps.Notes, hub: deps.Hub, tokens: deps.DevTokens,
		logger: logger, wsOrigins: deps.AllowedOrigins,
	}
	e.GET("/healthz", h.healthz)

	// Authentication is applied per route rather than with Group.Use, because a
	// group middleware also catches unknown paths and would turn the missing
	// dev-token route in jwt mode into a 401 instead of a 404.
	requireUser := authenticate(deps.Verifier, false)
	v1 := e.Group("/api/v1")
	if deps.DevTokens != nil {
		v1.POST("/auth/dev-token", h.devToken)
	}
	v1.POST("/notes", h.createNote, requireUser)
	v1.GET("/notes", h.listNotes, requireUser)
	v1.GET("/notes/:id", h.getNote, requireUser)
	v1.PUT("/notes/:id", h.updateNote, requireUser)
	v1.DELETE("/notes/:id", h.deleteNote, requireUser)
	v1.GET("/folders", h.listFolders, requireUser)
	if deps.Hub != nil {
		v1.GET("/ws", h.watch, authenticate(deps.Verifier, true))
	}

	return e
}

func corsMiddleware(origins []string) echo.MiddlewareFunc {
	return middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:  origins,
		AllowMethods:  []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders:  []string{echo.HeaderAuthorization, echo.HeaderContentType, "If-Match", "If-None-Match"},
		ExposeHeaders: []string{"ETag", echo.HeaderLocation},
	})
}

func requestLogger(logger *slog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:    true,
		LogURI:       true,
		LogStatus:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogError:     true,
		HandleError:  true, // route errors through the central handler so the logged status is the real one
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []any{
				"request_id", v.RequestID,
				"method", v.Method,
				"uri", redactURI(v.URI), // access_token is a credential
				"status", v.Status,
				"latency", v.Latency,
			}
			if v.Error != nil {
				attrs = append(attrs, "error", v.Error)
			}
			logger.Info("request", attrs...)
			return nil
		},
	})
}

type handler struct {
	pinger Pinger
	notes  *note.Service
	hub    *events.Hub
	tokens TokenIssuer
	logger *slog.Logger
	// wsOrigins are extra Origin patterns the WebSocket upgrade accepts.
	wsOrigins []string
}

func (h *handler) healthz(c echo.Context) error {
	// Bound the ping so a hung database makes the probe fail fast instead of hanging.
	ctx, cancel := context.WithTimeout(c.Request().Context(), healthTimeout)
	defer cancel()

	if err := h.pinger.Ping(ctx); err != nil {
		return c.JSON(http.StatusServiceUnavailable, errorBody{
			Error: errorDetail{Code: "unavailable", Message: "database unreachable"},
		})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
