package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/ryankupk/notes/internal/note"
)

// errorBody is the single error envelope every endpoint returns.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// conflictBody is the 412 envelope: the usual error plus the current note so
// the client can diff and retry.
type conflictBody struct {
	Error   errorDetail  `json:"error"`
	Current noteResponse `json:"current"`
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

// newErrorHandler is the central Echo error handler. Handlers just return the
// service's errors; this maps domain sentinels (and echo's own HTTPErrors) to
// a status and the single JSON envelope.
func newErrorHandler(logger *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		var conflict *note.ConflictError
		if errors.As(err, &conflict) {
			c.Response().Header().Set("ETag", etag(conflict.Current.Version))
			writeJSON(c, logger, http.StatusPreconditionFailed, conflictBody{
				Error:   errorDetail{Code: codeForStatus(http.StatusPreconditionFailed), Message: "note was modified by someone else"},
				Current: newNoteResponse(conflict.Current, true),
			})
			return
		}

		status := http.StatusInternalServerError
		message := "internal server error"

		var httpErr *echo.HTTPError
		code := ""
		switch {
		case errors.Is(err, note.ErrNotFound):
			status, message = http.StatusNotFound, "note not found"
		case errors.Is(err, note.ErrPathTaken):
			status, code, message = http.StatusConflict, "path_taken", "a note already exists at that path"
		case errors.Is(err, note.ErrInvalid):
			// Validation messages are written for clients and safe to expose.
			status, message = http.StatusBadRequest, validationMessage(err)
		case errors.As(err, &httpErr):
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

		if code == "" {
			code = codeForStatus(status)
		}
		writeJSON(c, logger, status, errorBody{Error: errorDetail{Code: code, Message: message}})
	}
}

func writeJSON(c echo.Context, logger *slog.Logger, status int, body any) {
	var err error
	if c.Request().Method == http.MethodHead {
		err = c.NoContent(status)
	} else {
		err = c.JSON(status, body)
	}
	if err != nil {
		logger.Error("writing error response", "error", err)
	}
}

// validationMessage drops the wrapping context and the sentinel prefix, leaving
// only the human-readable reason ("path is empty").
func validationMessage(err error) string {
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, note.ErrInvalid.Error()+": "); ok {
		return after
	}
	return msg
}
