package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/memstore"
)

type testEnv struct {
	t   *testing.T
	srv http.Handler
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	svc := note.NewService(memstore.New(), nil, nil, nil)
	srv := NewRouter(Deps{Pinger: fakePinger{}, Notes: svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &testEnv{t: t, srv: srv}
}

func (e *testEnv) do(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Current *noteResponse `json:"current"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return v
}

func (e *testEnv) create(path, body string, hdr ...string) noteResponse {
	e.t.Helper()
	payload := fmt.Sprintf(`{"path":%q,"body":%q,"tags":["a"]}`, path, body)
	rec := e.do(http.MethodPost, "/api/v1/notes", payload, hdr...)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create %s: status %d: %s", path, rec.Code, rec.Body)
	}
	return decode[noteResponse](e.t, rec)
}

const notesURL = "/api/v1/notes"

func TestCreate(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodPost, notesURL, `{"path":"/eng//deploy.md","body":"hi","tags":["B","a"]}`, userHeader, "alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	n := decode[noteResponse](t, rec)
	if rec.Header().Get("ETag") != `"1"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	if got, want := rec.Header().Get("Location"), "/api/v1/notes/"+n.ID; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if n.Path != "eng/deploy.md" || n.Title != "deploy.md" || n.Version != 1 || n.Body == nil || *n.Body != "hi" {
		t.Errorf("unexpected note %+v", n)
	}
	if strings.Join(n.Tags, ",") != "a,b" {
		t.Errorf("tags = %v", n.Tags)
	}
	if n.CreatedBy != "alice" || n.UpdatedBy != "alice" {
		t.Errorf("actor = %q/%q", n.CreatedBy, n.UpdatedBy)
	}
}

func TestCreateErrors(t *testing.T) {
	big := `{"path":"a.md","body":"` + strings.Repeat("x", 2<<20) + `"}`
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"malformed json", `{"path":`, 400, "invalid_request"},
		{"unknown field", `{"path":"a.md","bogus":1}`, 400, "invalid_request"},
		{"trailing data", `{"path":"a.md"}{}`, 400, "invalid_request"},
		{"empty path", `{"path":"  /"}`, 400, "invalid_request"},
		{"dotdot path", `{"path":"a/../b"}`, 400, "invalid_request"},
		{"too large", big, 413, "payload_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newEnv(t).do(http.MethodPost, notesURL, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body)
			}
			if got := decode[errResp](t, rec).Error.Code; got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
		})
	}

	t.Run("invalid path message is the validation reason", func(t *testing.T) {
		rec := newEnv(t).do(http.MethodPost, notesURL, `{"path":"a/../b"}`)
		if msg := decode[errResp](t, rec).Error.Message; strings.Contains(msg, "invalid input") || !strings.Contains(msg, "..") {
			t.Errorf("message = %q", msg)
		}
	})

	t.Run("duplicate path", func(t *testing.T) {
		e := newEnv(t)
		e.create("a.md", "x")
		rec := e.do(http.MethodPost, notesURL, `{"path":"a.md"}`)
		if rec.Code != 409 || decode[errResp](t, rec).Error.Code != "path_taken" {
			t.Errorf("got %d %s", rec.Code, rec.Body)
		}
	})
}

func TestGet(t *testing.T) {
	e := newEnv(t)
	n := e.create("a.md", "")
	url := notesURL + "/" + n.ID

	rec := e.do(http.MethodGet, url, "")
	if rec.Code != 200 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("got %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if !strings.Contains(rec.Body.String(), `"body":""`) {
		t.Errorf("empty body should serialize: %s", rec.Body)
	}

	for _, inm := range []string{`"1"`, `W/"1"`, `"9", "1"`, `*`} {
		rec = e.do(http.MethodGet, url, "", "If-None-Match", inm)
		if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != `"1"` {
			t.Errorf("If-None-Match %s: got %d body %q", inm, rec.Code, rec.Body)
		}
	}
	if rec = e.do(http.MethodGet, url, "", "If-None-Match", `"2"`); rec.Code != 200 {
		t.Errorf("non-matching If-None-Match: %d", rec.Code)
	}
	if rec = e.do(http.MethodGet, notesURL+"/missing", ""); rec.Code != 404 || decode[errResp](t, rec).Error.Code != "not_found" {
		t.Errorf("missing: %d %s", rec.Code, rec.Body)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	n := e.create("eng/a.md", "v1", userHeader, "alice")
	url := notesURL + "/" + n.ID

	rec := e.do(http.MethodPut, url, `{"path":"ops/b.md","body":"v2","tags":[]}`, "If-Match", `"1"`, userHeader, "bob")
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("got %d etag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := decode[noteResponse](t, rec)
	if got.ID != n.ID || got.Path != "ops/b.md" || got.Title != "b.md" || got.Version != 2 {
		t.Errorf("rename failed: %+v", got)
	}
	if got.CreatedBy != "alice" || got.UpdatedBy != "bob" {
		t.Errorf("actors = %q/%q", got.CreatedBy, got.UpdatedBy)
	}
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("tags should be empty array, got %v", got.Tags)
	}

	// Bare integer If-Match is accepted too.
	if rec = e.do(http.MethodPut, url, `{"path":"ops/b.md","body":"v3"}`, "If-Match", "2"); rec.Code != 200 {
		t.Errorf("bare If-Match: %d %s", rec.Code, rec.Body)
	}
}

func TestPreconditions(t *testing.T) {
	e := newEnv(t)
	n := e.create("a.md", "x")
	url := notesURL + "/" + n.ID
	body := `{"path":"a.md","body":"y"}`

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := e.do(method, url, body)
			if rec.Code != 428 || decode[errResp](t, rec).Error.Code != "precondition_required" {
				t.Errorf("missing If-Match: %d %s", rec.Code, rec.Body)
			}
			for _, bad := range []string{`W/"1"`, `abc`, `"1","2"`, `*`, `-1`, `"1`} {
				rec = e.do(method, url, body, "If-Match", bad)
				if rec.Code != 400 || decode[errResp](t, rec).Error.Code != "invalid_request" {
					t.Errorf("If-Match %s: %d %s", bad, rec.Code, rec.Body)
				}
			}
			rec = e.do(method, notesURL+"/missing", body, "If-Match", `"1"`)
			if rec.Code != 404 {
				t.Errorf("missing note: %d", rec.Code)
			}
		})
	}
}

func TestStaleWriteReturns412WithCurrent(t *testing.T) {
	e := newEnv(t)
	n := e.create("a.md", "x")
	url := notesURL + "/" + n.ID
	if rec := e.do(http.MethodPut, url, `{"path":"a.md","body":"second"}`, "If-Match", `"1"`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}

	for _, tc := range []struct{ method, body string }{
		{http.MethodPut, `{"path":"a.md","body":"stale"}`},
		{http.MethodDelete, ""},
	} {
		rec := e.do(tc.method, url, tc.body, "If-Match", `"1"`)
		if rec.Code != 412 {
			t.Fatalf("%s: status = %d: %s", tc.method, rec.Code, rec.Body)
		}
		if rec.Header().Get("ETag") != `"2"` {
			t.Errorf("%s: ETag = %q", tc.method, rec.Header().Get("ETag"))
		}
		er := decode[errResp](t, rec)
		if er.Error.Code != "version_conflict" || er.Current == nil || er.Current.Version != 2 ||
			er.Current.Body == nil || *er.Current.Body != "second" {
			t.Errorf("%s: body = %s", tc.method, rec.Body)
		}
	}
}

func TestUpdateToTakenPath(t *testing.T) {
	e := newEnv(t)
	e.create("a.md", "")
	b := e.create("b.md", "")
	rec := e.do(http.MethodPut, notesURL+"/"+b.ID, `{"path":"a.md"}`, "If-Match", `"1"`)
	if rec.Code != 409 || decode[errResp](t, rec).Error.Code != "path_taken" {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	n := e.create("a.md", "x")
	url := notesURL + "/" + n.ID
	rec := e.do(http.MethodDelete, url, "", "If-Match", `"1"`, userHeader, "carol")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if rec = e.do(http.MethodGet, url, ""); rec.Code != 404 {
		t.Errorf("get after delete: %d", rec.Code)
	}
	// Tombstone is visible with includeDeleted and records who deleted it.
	list := decode[listResponse](t, e.do(http.MethodGet, notesURL+"?includeDeleted=true", ""))
	if len(list.Items) != 1 || !list.Items[0].Deleted || list.Items[0].UpdatedBy != "carol" {
		t.Errorf("tombstone = %+v", list.Items)
	}
	if got := decode[listResponse](t, e.do(http.MethodGet, notesURL, "")); len(got.Items) != 0 {
		t.Errorf("deleted note listed: %+v", got.Items)
	}
}

func TestListQueryErrors(t *testing.T) {
	good := base64.RawURLEncoding.EncodeToString([]byte("2024-01-01T00:00:00Z|x"))
	for name, query := range map[string]string{
		"bad limit":           "limit=abc",
		"negative limit":      "limit=-1",
		"bad cursor base64":   "cursor=!!!",
		"cursor no separator": "cursor=" + base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"cursor bad time":     "cursor=" + base64.RawURLEncoding.EncodeToString([]byte("yesterday|x")),
		"padded cursor":       "cursor=" + good + "==",
		"bad updatedSince":    "updatedSince=yesterday",
		"bad includeDeleted":  "includeDeleted=maybe",
		"bad fields":          "fields=secret",
	} {
		t.Run(name, func(t *testing.T) {
			rec := newEnv(t).do(http.MethodGet, notesURL+"?"+query, "")
			if rec.Code != 400 || decode[errResp](t, rec).Error.Code != "invalid_request" {
				t.Errorf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestListEmptyIsArray(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, notesURL, "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

func TestListFilters(t *testing.T) {
	e := newEnv(t)
	e.create("eng/a.md", "kubernetes rollout")
	e.create("eng/b.md", "other")
	e.create("ops/c.md", "kubernetes")
	rec := e.do(http.MethodPost, notesURL, `{"path":"ops/d.md","tags":["Special"]}`)
	if rec.Code != 201 {
		t.Fatal(rec.Body)
	}

	paths := func(query string) string {
		t.Helper()
		rec := e.do(http.MethodGet, notesURL+"?"+query, "")
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		var ps []string
		for _, it := range decode[listResponse](t, rec).Items {
			if it.Body != nil {
				t.Errorf("%s: body should be omitted", query)
			}
			ps = append(ps, it.Path)
		}
		slices.Sort(ps) // same-millisecond writes tie on updatedAt, then order by random id
		return strings.Join(ps, ",")
	}
	tests := map[string]string{
		"prefix=eng/":                       "eng/a.md,eng/b.md",
		"tag=special":                       "ops/d.md",
		"q=kubernetes":                      "eng/a.md,ops/c.md",
		"prefix=ops/&q=kubernetes":          "ops/c.md",
		"updatedSince=2999-01-01T00:00:00Z": "",
		"updatedSince=2000-01-01T00:00:00.123456789Z&prefix=eng/b": "eng/b.md",
	}
	for q, want := range tests {
		if got := paths(q); got != want {
			t.Errorf("%s: got %q, want %q", q, got, want)
		}
	}

	withBody := decode[listResponse](t, e.do(http.MethodGet, notesURL+"?prefix=eng/a&fields=body", ""))
	if len(withBody.Items) != 1 || withBody.Items[0].Body == nil || *withBody.Items[0].Body != "kubernetes rollout" {
		t.Errorf("fields=body: %+v", withBody.Items)
	}
}

func TestListPagination(t *testing.T) {
	// A fixed clock forces identical updatedAt values, so ordering and cursors depend on the id tiebreak.
	at := time.Date(2024, 5, 1, 12, 0, 0, 123456789, time.UTC)
	svc := note.NewService(memstore.New(), nil, func() time.Time { return at }, nil)
	e := &testEnv{t: t, srv: NewRouter(Deps{Pinger: fakePinger{}, Notes: svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})}
	for i := range 5 {
		e.create(fmt.Sprintf("n%d.md", i), "")
	}

	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		url := notesURL + "?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp := decode[listResponse](t, e.do(http.MethodGet, url, ""))
		pages++
		for _, it := range resp.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate %s across pages", it.ID)
			}
			seen[it.ID] = true
		}
		if resp.NextCursor == "" {
			break
		}
		if strings.ContainsAny(resp.NextCursor, "=+/") {
			t.Errorf("cursor not raw base64url: %q", resp.NextCursor)
		}
		cursor = resp.NextCursor
	}
	if len(seen) != 5 || pages != 3 {
		t.Errorf("saw %d notes over %d pages, want 5 over 3", len(seen), pages)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	in := note.Cursor{UpdatedAt: time.Date(2024, 1, 2, 3, 4, 5, 6000000, time.UTC), ID: "abc"}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil || !out.UpdatedAt.Equal(in.UpdatedAt) || out.ID != in.ID {
		t.Errorf("got %+v, %v", out, err)
	}
}

func TestFolders(t *testing.T) {
	e := newEnv(t)
	e.create("eng/a.md", "")
	e.create("eng/runbooks/b.md", "")
	e.create("eng/runbooks/c.md", "")
	e.create("top.md", "")

	rec := e.do(http.MethodGet, "/api/v1/folders?prefix=eng", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := decode[foldersResponse](t, rec)
	if got.Prefix != "eng/" || len(got.Folders) != 1 ||
		got.Folders[0] != (folderResponse{Path: "eng/runbooks/", Name: "runbooks", NoteCount: 2}) {
		t.Errorf("got %+v", got)
	}

	root := decode[foldersResponse](t, e.do(http.MethodGet, "/api/v1/folders", ""))
	if root.Prefix != "" || len(root.Folders) != 1 || root.Folders[0].Name != "eng" {
		t.Errorf("root = %+v", root)
	}
	empty := e.do(http.MethodGet, "/api/v1/folders?prefix=nope/", "")
	if !strings.Contains(empty.Body.String(), `"folders":[]`) {
		t.Errorf("empty folders: %s", empty.Body)
	}
	if rec = e.do(http.MethodGet, "/api/v1/folders?prefix=a/../b", ""); rec.Code != 400 {
		t.Errorf("bad prefix: %d", rec.Code)
	}
}

func TestDefaultActorIsAnonymous(t *testing.T) {
	n := newEnv(t).create("a.md", "")
	if n.CreatedBy != "anonymous" {
		t.Errorf("createdBy = %q", n.CreatedBy)
	}
}
