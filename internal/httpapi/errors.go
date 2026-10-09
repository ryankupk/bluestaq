package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// errorBody is the single error envelope every endpoint returns.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// codeForStatus gives clients a stable machine-readable code alongside the HTTP status.
func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "version_conflict"
	case http.StatusPreconditionRequired:
		return "precondition_required"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		if status >= 500 {
			return "internal"
		}
		return "error"
	}
}

// newErrorHandler is the central Echo error handler. Later phases translate
// domain errors into *echo.HTTPError (or extend this function) so every
// failure leaves the service in the same JSON shape.
func newErrorHandler(logger *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		status := http.StatusInternalServerError
		message := "internal server error"

		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.Code
			// Only echo's own client-facing messages are safe to forward; 5xx details stay in logs.
			if status < 500 {
				if text, ok := httpErr.Message.(string); ok {
					message = text
				} else {
					message = http.StatusText(status)
				}
			} else {
				message = http.StatusText(status)
			}
		}
		if status >= 500 {
			logger.Error("request failed", "error", err, "path", c.Request().URL.Path)
		}

		body := errorBody{Error: errorDetail{Code: codeForStatus(status), Message: message}}
		var writeErr error
		if c.Request().Method == http.MethodHead {
			writeErr = c.NoContent(status)
		} else {
			writeErr = c.JSON(status, body)
		}
		if writeErr != nil {
			logger.Error("writing error response", "error", writeErr)
		}
	}
}
