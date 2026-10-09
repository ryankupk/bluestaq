package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ryankupk/notes/internal/auth"
	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/memstore"
)

func TestRESTRequiresValidToken(t *testing.T) {
	e := newEnv(t)
	routes := []struct{ method, target, body string }{
		{http.MethodPost, notesURL, `{"path":"a.md"}`},
		{http.MethodGet, notesURL, ""},
		{http.MethodGet, notesURL + "/abc", ""},
		{http.MethodPut, notesURL + "/abc", `{"path":"a.md"}`},
		{http.MethodDelete, notesURL + "/abc", ""},
		{http.MethodGet, "/api/v1/folders", ""},
	}
	for name, credentials := range badCredentials(t) {
		for _, r := range routes {
			t.Run(name+" "+r.method+" "+r.target, func(t *testing.T) {
				rec := e.do(r.method, r.target, r.body, "Authorization", credentials, "If-Match", `"1"`)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
				}
				if got := decode[errResp](t, rec).Error.Code; got != "unauthorized" {
					t.Errorf("code = %q, want unauthorized", got)
				}
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("WWW-Authenticate = %q, want Bearer", got)
				}
			})
		}
	}
}

func TestHealthzStaysPublic(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(fakePinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRESTIgnoresQueryToken(t *testing.T) {
	// Only the WebSocket route may take a token from the URL.
	e := newEnv(t)
	token := strings.TrimPrefix(e.as("alice"), "Bearer ")
	rec := e.do(http.MethodGet, notesURL+"?access_token="+token, "", "Authorization", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNilVerifierRejectsEverything(t *testing.T) {
	srv := NewRouter(Deps{Pinger: fakePinger{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	req := httptest.NewRequest(http.MethodGet, notesURL, nil)
	req.Header.Set("Authorization", bearerFor(t, "alice"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, notesURL, "", "Authorization", "bearer "+strings.TrimPrefix(e.as("alice"), "Bearer "))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestDevToken(t *testing.T) {
	e := newEnv(t)
	const target = "/api/v1/auth/dev-token"

	t.Run("success needs no credentials", func(t *testing.T) {
		rec := e.do(http.MethodPost, target, `{"user":"  alice  "}`, "Authorization", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		got := decode[devTokenResponse](t, rec)
		if got.User != "alice" {
			t.Errorf("user = %q, want trimmed alice", got.User)
		}
		expiresAt, err := time.Parse(time.RFC3339, got.ExpiresAt)
		if err != nil || time.Until(expiresAt) < 50*time.Minute {
			t.Errorf("expiresAt = %q (%v)", got.ExpiresAt, err)
		}
		// The token must actually work against the protected API as that user.
		created := decode[noteResponse](t, e.do(http.MethodPost, notesURL, `{"path":"a.md"}`, "Authorization", "Bearer "+got.Token))
		if created.CreatedBy != "alice" {
			t.Errorf("createdBy = %q, want alice", created.CreatedBy)
		}
	})

	t.Run("accepts 64 characters, counted as characters", func(t *testing.T) {
		name := strings.Repeat("é", 64)
		if rec := e.do(http.MethodPost, target, `{"user":"`+name+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})

	invalid := map[string]string{
		"empty":           `{"user":""}`,
		"blank":           `{"user":"   "}`,
		"missing":         `{}`,
		"too long":        `{"user":"` + strings.Repeat("a", 65) + `"}`,
		"newline":         `{"user":"al\nice"}`,
		"tab":             `{"user":"al\tice"}`,
		"nul":             `{"user":"al\u0000ice"}`,
		"not json":        `alice`,
		"unknown field":   `{"user":"alice","admin":true}`,
		"trailing data":   `{"user":"alice"}{}`,
		"wrong user type": `{"user":5}`,
	}
	for name, body := range invalid {
		t.Run("rejects "+name, func(t *testing.T) {
			rec := e.do(http.MethodPost, target, body)
			if rec.Code != http.StatusBadRequest || decode[errResp](t, rec).Error.Code != "invalid_request" {
				t.Fatalf("got %d %s, want 400 invalid_request", rec.Code, rec.Body)
			}
		})
	}
}

func TestDevTokenAbsentInJWTMode(t *testing.T) {
	srv := NewRouter(Deps{
		Pinger: fakePinger{}, Notes: note.NewService(memstore.New(), nil, nil, nil),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Verifier: newTestTokens(t),
	})
	for _, credentials := range []string{"", bearerFor(t, "alice")} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev-token", strings.NewReader(`{"user":"alice"}`))
		if credentials != "" {
			req.Header.Set("Authorization", credentials)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("credentials %q: status = %d, want 404", credentials, rec.Code)
		}
	}
}

func TestCORS(t *testing.T) {
	const allowed = "http://localhost:5173"
	newRouter := func(origins ...string) http.Handler {
		tokens := newTestTokens(t)
		return NewRouter(Deps{
			Pinger: fakePinger{}, Notes: note.NewService(memstore.New(), nil, nil, nil),
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			Verifier: tokens, AllowedOrigins: origins,
		})
	}
	preflight := func(srv http.Handler, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, notesURL, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "PUT")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,if-match")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	t.Run("preflight from allowed origin succeeds without a token", func(t *testing.T) {
		rec := preflight(newRouter(allowed), allowed)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
		h := rec.Header()
		if h.Get("Access-Control-Allow-Origin") != allowed {
			t.Errorf("Allow-Origin = %q", h.Get("Access-Control-Allow-Origin"))
		}
		for _, m := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"} {
			if !strings.Contains(h.Get("Access-Control-Allow-Methods"), m) {
				t.Errorf("Allow-Methods %q lacks %s", h.Get("Access-Control-Allow-Methods"), m)
			}
		}
		for _, name := range []string{"Authorization", "Content-Type", "If-Match", "If-None-Match"} {
			if !strings.Contains(h.Get("Access-Control-Allow-Headers"), name) {
				t.Errorf("Allow-Headers %q lacks %s", h.Get("Access-Control-Allow-Headers"), name)
			}
		}
	})

	t.Run("preflight from other origin gets no allow headers", func(t *testing.T) {
		rec := preflight(newRouter(allowed), "http://evil.example")
		for name := range rec.Header() {
			if strings.HasPrefix(name, "Access-Control-Allow") {
				t.Errorf("unexpected %s: %q", name, rec.Header().Get(name))
			}
		}
	})

	t.Run("no origins configured means no CORS headers", func(t *testing.T) {
		rec := preflight(newRouter(), allowed)
		for name := range rec.Header() {
			if strings.HasPrefix(name, "Access-Control-") {
				t.Errorf("unexpected %s", name)
			}
		}
	})

	t.Run("actual response exposes ETag and Location", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("Origin", allowed)
		rec := httptest.NewRecorder()
		newRouter(allowed).ServeHTTP(rec, req)
		exposed := rec.Header().Get("Access-Control-Expose-Headers")
		if !strings.Contains(exposed, "ETag") || !strings.Contains(exposed, "Location") {
			t.Errorf("Expose-Headers = %q", exposed)
		}
	})
}

func TestRedactURI(t *testing.T) {
	tests := map[string]string{
		"/api/v1/notes":                              "/api/v1/notes",
		"/api/v1/notes?prefix=eng/":                  "/api/v1/notes?prefix=eng/",
		"/api/v1/ws?access_token=abc.def.ghi":        "/api/v1/ws?access_token=REDACTED",
		"/api/v1/ws?prefix=eng/&access_token=secret": "/api/v1/ws?prefix=eng/&access_token=REDACTED",
		"/api/v1/ws?access_token=a&access_token=b":   "/api/v1/ws?access_token=REDACTED&access_token=REDACTED",
		"/api/v1/ws?access%5Ftoken=secret":           "/api/v1/ws?access%5Ftoken=REDACTED",
		"/api/v1/ws?my_access_token=keep":            "/api/v1/ws?my_access_token=keep",
	}
	for in, want := range tests {
		if got := redactURI(in); got != want {
			t.Errorf("redactURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestLogDoesNotLeakAccessToken(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	tokens := newTestTokens(t)
	srv := httptest.NewServer(NewRouter(Deps{
		Pinger: fakePinger{}, Notes: note.NewService(memstore.New(), nil, nil, nil),
		Logger: logger, Verifier: tokens,
	}))
	defer srv.Close()

	const secret = "super-secret-token-value"
	resp, err := http.Get(srv.URL + notesURL + "?access_token=" + secret)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	out := logs.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaked the token: %s", out)
	}
	if !strings.Contains(out, "access_token=REDACTED") {
		t.Errorf("log should show the redacted URI: %s", out)
	}
}

func TestWSRequiresValidToken(t *testing.T) {
	e := newWSEnv(t)
	base := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws"

	dialStatus := func(t *testing.T, url string, opts *websocket.DialOptions) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, resp, err := websocket.Dial(ctx, url, opts)
		if err == nil {
			conn.CloseNow()
			return 0, ""
		}
		if resp == nil {
			t.Fatalf("dial failed without a response: %v", err)
		}
		return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
	}

	for name, credentials := range badCredentials(t) {
		t.Run("header "+name, func(t *testing.T) {
			opts := &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {credentials}}}
			if status, challenge := dialStatus(t, base, opts); status != http.StatusUnauthorized || challenge != "Bearer" {
				t.Fatalf("status = %d, WWW-Authenticate = %q; want 401 Bearer", status, challenge)
			}
		})
		t.Run("query "+name, func(t *testing.T) {
			url := base
			if token := strings.TrimPrefix(credentials, "Bearer "); credentials != "" && token != credentials {
				url += "?access_token=" + token
			}
			if status, _ := dialStatus(t, url, nil); status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", status)
			}
		})
	}
}

func TestWSIdentityComesOnlyFromToken(t *testing.T) {
	e := newWSEnv(t)
	base := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws"
	hello := func(t *testing.T, url string, opts *websocket.DialOptions) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, url, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		var f frame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			t.Fatal(err)
		}
		return f.User
	}

	// The ?user= parameter and X-User-ID header no longer mean anything.
	if got := hello(t, e.wsURL("alice", "user=mallory"), &websocket.DialOptions{HTTPHeader: http.Header{"X-User-Id": {"eve"}}}); got != "alice" {
		t.Errorf("query token: hello user = %q, want alice", got)
	}
	// Non-browser clients may use the Authorization header, which wins over the query.
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {bearerFor(t, "bob")}}}
	if got := hello(t, e.wsURL("alice", ""), opts); got != "bob" {
		t.Errorf("header token: hello user = %q, want bob", got)
	}
	if got := hello(t, base, opts); got != "bob" {
		t.Errorf("header-only token: hello user = %q, want bob", got)
	}
}

func TestWSOriginCheck(t *testing.T) {
	const allowed = "http://localhost:5173"
	e := newWSEnv(t, allowed)
	url := e.wsURL("alice", "")

	tests := []struct {
		name   string
		origin string
		want   int // 0 means the upgrade succeeds
	}{
		{"allowed origin", allowed, 0},
		{"allowed host with other scheme", "https://localhost:5173", http.StatusForbidden},
		{"disallowed origin", "http://evil.example", http.StatusForbidden},
		{"same origin", e.srv.URL, 0},
		{"no origin header (non-browser client)", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
			if tt.origin != "" {
				opts.HTTPHeader.Set("Origin", tt.origin)
			}
			conn, resp, err := websocket.Dial(ctx, url, opts)
			if tt.want == 0 {
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				conn.CloseNow()
				return
			}
			if err == nil {
				conn.CloseNow()
				t.Fatal("upgrade succeeded, want rejection")
			}
			if resp == nil || resp.StatusCode != tt.want {
				t.Fatalf("resp = %v, want status %d", resp, tt.want)
			}
		})
	}
}

// Compile-time check that the real verifier satisfies the seam the router depends on.
var _ auth.Verifier = (*auth.HS256)(nil)
