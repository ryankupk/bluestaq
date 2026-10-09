package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newTestRouter(p Pinger) http.Handler {
	return NewRouter(Deps{Pinger: p, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
		wantCode   string
	}{
		{name: "healthy", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "mongo down", pingErr: errors.New("boom"), wantStatus: http.StatusServiceUnavailable, wantCode: "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newTestRouter(fakePinger{tt.pingErr}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("missing X-Request-Id header")
			}
			var body struct {
				Status string `json:"status"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
			}
			if body.Status != tt.wantBody || body.Error.Code != tt.wantCode {
				t.Errorf("body = %+v, want status=%q code=%q", body, tt.wantBody, tt.wantCode)
			}
		})
	}
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(fakePinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("body = %s, want not_found envelope", rec.Body.String())
	}
}
