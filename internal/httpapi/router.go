// Package httpapi is the HTTP transport layer: routing, middleware, and handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Pinger reports whether a backing dependency is reachable. It is declared here,
// where it is consumed, so health checks can be tested without MongoDB.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators the router needs. Later phases add the note service and event hub.
type Deps struct {
	Pinger Pinger
	Logger *slog.Logger
}

const (
	bodyLimit     = "1M"
	healthTimeout = 2 * time.Second
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
	e.Use(middleware.BodyLimit(bodyLimit))

	h := &handler{pinger: deps.Pinger}
	e.GET("/healthz", h.healthz)

	return e
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
				"uri", v.URI,
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
