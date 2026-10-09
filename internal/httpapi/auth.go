package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/ryankupk/notes/internal/auth"
)

const (
	// userContextKey holds the verified user name in the echo context. Only
	// the authenticate middleware writes it and only actor reads it.
	userContextKey = "auth.user"

	// accessTokenParam carries the token on the WebSocket handshake, where
	// browsers cannot set an Authorization header.
	accessTokenParam = "access_token"

	maxUserNameChars = 64
)

// TokenIssuer mints tokens for the dev-token endpoint. It is only wired in dev mode.
type TokenIssuer interface {
	Issue(user string) (token string, expiresAt time.Time, err error)
}

// actor is the single place the caller's identity is read. It is whatever the
// authenticate middleware verified, so handlers never see client-supplied identity.
func actor(c echo.Context) string {
	user, _ := c.Get(userContextKey).(string)
	return user
}

// authenticate rejects requests without a valid bearer token and records the
// verified user for actor. allowQueryToken additionally accepts ?access_token=,
// which only the WebSocket route enables: a token in a URL ends up in logs and
// history, so REST routes never read it.
//
// A nil verifier rejects everything (fail closed) instead of letting requests through.
func authenticate(verifier auth.Verifier, allowQueryToken bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := bearerToken(c.Request())
			if token == "" && allowQueryToken {
				token = c.QueryParam(accessTokenParam)
			}
			if token == "" {
				return unauthorized("missing bearer token")
			}
			if verifier == nil {
				return unauthorized("invalid or expired token")
			}
			user, err := verifier.Verify(token)
			if err != nil {
				// The reason (bad signature, expired, ...) stays out of the response.
				return unauthorized("invalid or expired token")
			}
			c.Set(userContextKey, user)
			return next(c)
		}
	}
}

func unauthorized(message string) error {
	return echo.NewHTTPError(http.StatusUnauthorized, message)
}

// bearerToken extracts the token from "Authorization: Bearer <token>", or "" if absent or malformed.
func bearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get(echo.HeaderAuthorization), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

type devTokenRequest struct {
	User string `json:"user"`
}

type devTokenResponse struct {
	Token     string `json:"token"`
	User      string `json:"user"`
	ExpiresAt string `json:"expiresAt"`
}

// devToken lets a demo client sign in by typing a name. It is registered only
// in dev mode; in jwt mode the route does not exist.
func (h *handler) devToken(c echo.Context) error {
	var req devTokenRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	user, err := validateUserName(req.User)
	if err != nil {
		return err
	}
	token, expiresAt, err := h.tokens.Issue(user)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, devTokenResponse{
		Token: token, User: user, ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// validateUserName trims the name and enforces 1-64 characters with no control
// characters, so a name is safe to log and to show in other users' UIs.
func validateUserName(raw string) (string, error) {
	user := strings.TrimSpace(raw)
	switch {
	case user == "":
		return "", badRequest("user is required")
	case utf8.RuneCountInString(user) > maxUserNameChars:
		return "", badRequest("user must be at most %d characters", maxUserNameChars)
	case strings.IndexFunc(user, unicode.IsControl) >= 0:
		return "", badRequest("user must not contain control characters")
	}
	return user, nil
}

// redactURI hides access_token values so request logs never contain a usable credential.
func redactURI(uri string) string {
	path, query, found := strings.Cut(uri, "?")
	if !found {
		return uri
	}
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		key, _, _ := strings.Cut(pair, "=")
		// Decode the key: ?access%5Ftoken=... is the same parameter to the server.
		if decoded, err := url.QueryUnescape(key); err == nil && decoded == accessTokenParam {
			pairs[i] = key + "=REDACTED"
		}
	}
	return path + "?" + strings.Join(pairs, "&")
}
