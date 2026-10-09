// Package e2e holds black-box contract tests: the production wiring (internal/app)
// on a throwaway MongoDB database, served over httptest and driven only through
// HTTP and a WebSocket client.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ryankupk/notes/internal/app"
)

// noteJSON is the wire shape of a note as the tests see it.
type noteJSON struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Body      *string   `json:"body"`
	Tags      []string  `json:"tags"`
	Version   int64     `json:"version"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
	CreatedBy string    `json:"createdBy"`
}

type listJSON struct {
	Items      []noteJSON `json:"items"`
	NextCursor string     `json:"nextCursor"`
}

// env is a running app on a fresh database.
type env struct {
	t   *testing.T
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI is not set; skipping e2e tests (try MONGO_URI=mongodb://localhost:27017)")
	}
	if testing.Short() {
		t.Skip("-short set; skipping e2e tests")
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	db := client.Database(fmt.Sprintf("notes_e2e_%d_%s", time.Now().UnixNano(), hex.EncodeToString(suffix)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service, err := app.New(ctx, db.Collection("notes"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = client.Disconnect(ctx)
		t.Fatalf("app.New: %v", err)
	}
	srv := httptest.NewServer(service.Handler())

	// Cleanups run last-in first-out: close sockets, stop the server, then drop the data.
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		if err := db.Drop(dropCtx); err != nil {
			t.Errorf("dropping %s: %v", db.Name(), err)
		}
		if err := client.Disconnect(dropCtx); err != nil {
			t.Errorf("disconnect: %v", err)
		}
	})
	t.Cleanup(srv.Close)
	t.Cleanup(service.Close)
	return &env{t: t, srv: srv}
}

// do sends a request and returns status, headers and the raw body.
func (e *env) do(method, path, body string, headers ...string) (int, http.Header, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, reader)
	if err != nil {
		e.t.Fatalf("building request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decoding %q: %v", data, err)
	}
	return v
}

func (e *env) expect(want int, method, path, body string, headers ...string) (http.Header, []byte) {
	e.t.Helper()
	status, hdr, data := e.do(method, path, body, headers...)
	if status != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, data)
	}
	return hdr, data
}

func noteBody(path, body string, tags ...string) string {
	b, _ := json.Marshal(map[string]any{"path": path, "body": body, "tags": tags})
	return string(b)
}

// create POSTs a note and returns it with its ETag.
func (e *env) create(path, body, user string, tags ...string) (noteJSON, string) {
	e.t.Helper()
	hdr, data := e.expect(http.StatusCreated, http.MethodPost, "/api/v1/notes", noteBody(path, body, tags...), "X-User-ID", user)
	n := decode[noteJSON](e.t, data)
	if loc := hdr.Get("Location"); loc != "/api/v1/notes/"+n.ID {
		e.t.Fatalf("Location = %q", loc)
	}
	return n, hdr.Get("ETag")
}

func (e *env) list(query string) listJSON {
	e.t.Helper()
	_, data := e.expect(http.StatusOK, http.MethodGet, "/api/v1/notes?"+query, "")
	return decode[listJSON](e.t, data)
}

func paths(items []noteJSON) []string {
	out := make([]string, len(items))
	for i, n := range items {
		out[i] = n.Path
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	for _, p := range want {
		seen[p]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	_, data := e.expect(http.StatusOK, http.MethodGet, "/healthz", "")
	if got := decode[map[string]string](t, data); got["status"] != "ok" {
		t.Fatalf("healthz = %v", got)
	}
}

func TestCRUDLifecycle(t *testing.T) {
	e := newEnv(t)
	created, etag := e.create("eng/runbooks/deploy.md", "# Deploy", "alice", "Ops", "runbook")
	if etag != `"1"` || created.Version != 1 || created.Title != "deploy.md" || created.CreatedBy != "alice" ||
		strings.Join(created.Tags, ",") != "ops,runbook" || created.Body == nil || *created.Body != "# Deploy" {
		t.Fatalf("created = %+v etag=%s", created, etag)
	}
	notePath := "/api/v1/notes/" + created.ID

	hdr, data := e.expect(http.StatusOK, http.MethodGet, notePath, "")
	if hdr.Get("ETag") != `"1"` || decode[noteJSON](t, data).ID != created.ID {
		t.Fatalf("get etag=%s body=%s", hdr.Get("ETag"), data)
	}
	e.expect(http.StatusNotModified, http.MethodGet, notePath, "", "If-None-Match", `"1"`)

	// Updates require If-Match.
	e.expect(http.StatusPreconditionRequired, http.MethodPut, notePath, noteBody("eng/runbooks/deploy.md", "x"))
	hdr, data = e.expect(http.StatusOK, http.MethodPut, notePath, noteBody("eng/runbooks/deploy.md", "# Deploy v2", "ops"),
		"If-Match", `"1"`, "X-User-ID", "bob")
	updated := decode[noteJSON](t, data)
	if hdr.Get("ETag") != `"2"` || updated.Version != 2 || updated.UpdatedBy != "bob" || updated.CreatedBy != "alice" ||
		*updated.Body != "# Deploy v2" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("updated = %+v etag=%s", updated, hdr.Get("ETag"))
	}

	e.expect(http.StatusPreconditionRequired, http.MethodDelete, notePath, "")
	e.expect(http.StatusNoContent, http.MethodDelete, notePath, "", "If-Match", `"2"`, "X-User-ID", "alice")
	e.expect(http.StatusNotFound, http.MethodGet, notePath, "")
	e.expect(http.StatusNotFound, http.MethodPut, notePath, noteBody("eng/runbooks/deploy.md", "x"), "If-Match", `"3"`)

	// Validation errors use the shared envelope.
	_, data = e.expect(http.StatusBadRequest, http.MethodPost, "/api/v1/notes", noteBody("a/../b.md", ""))
	if got := decode[struct{ Error struct{ Code string } }](t, data); got.Error.Code != "invalid_request" {
		t.Fatalf("error body = %s", data)
	}
}

func TestTwoWriterConflict(t *testing.T) {
	e := newEnv(t)
	n, _ := e.create("shared/plan.md", "v1", "alice")
	notePath := "/api/v1/notes/" + n.ID

	// Both writers read v1.
	_, dataA := e.expect(http.StatusOK, http.MethodGet, notePath, "")
	_, dataB := e.expect(http.StatusOK, http.MethodGet, notePath, "")
	if decode[noteJSON](t, dataA).Version != 1 || decode[noteJSON](t, dataB).Version != 1 {
		t.Fatal("both writers should read version 1")
	}

	// A wins.
	e.expect(http.StatusOK, http.MethodPut, notePath, noteBody("shared/plan.md", "A's edit"), "If-Match", `"1"`, "X-User-ID", "alice")

	// B is rejected with the current note.
	_, data := e.expect(http.StatusPreconditionFailed, http.MethodPut, notePath, noteBody("shared/plan.md", "B's edit"),
		"If-Match", `"1"`, "X-User-ID", "bob")
	conflict := decode[struct {
		Error   struct{ Code string }
		Current noteJSON
	}](t, data)
	if conflict.Error.Code != "version_conflict" || conflict.Current.Version != 2 || *conflict.Current.Body != "A's edit" {
		t.Fatalf("conflict = %s", data)
	}

	// B retries on the new ETag and succeeds.
	hdr, _ := e.expect(http.StatusOK, http.MethodPut, notePath, noteBody("shared/plan.md", "A's edit + B's edit"),
		"If-Match", fmt.Sprintf(`"%d"`, conflict.Current.Version), "X-User-ID", "bob")
	if hdr.Get("ETag") != `"3"` {
		t.Fatalf("retry etag = %s", hdr.Get("ETag"))
	}
}

func TestRenameCollisionAndPathReuse(t *testing.T) {
	e := newEnv(t)
	a, _ := e.create("eng/a.md", "a", "alice")
	e.create("eng/b.md", "b", "alice")

	e.expect(http.StatusConflict, http.MethodPost, "/api/v1/notes", noteBody("eng/b.md", "dup"))

	// Renaming onto a taken path is a collision; to a free one, a move that keeps the ID.
	aPath := "/api/v1/notes/" + a.ID
	e.expect(http.StatusConflict, http.MethodPut, aPath, noteBody("eng/b.md", "a"), "If-Match", `"1"`)
	_, data := e.expect(http.StatusOK, http.MethodPut, aPath, noteBody("ops/renamed.md", "a"), "If-Match", `"1"`)
	moved := decode[noteJSON](t, data)
	if moved.ID != a.ID || moved.Path != "ops/renamed.md" || moved.Title != "renamed.md" || moved.Version != 2 {
		t.Fatalf("moved = %+v", moved)
	}

	// The old path is free again, and a deleted path can be reused too.
	reused, _ := e.create("eng/a.md", "new a", "alice")
	if reused.ID == a.ID {
		t.Fatal("reused path should be a new note")
	}
	e.expect(http.StatusNoContent, http.MethodDelete, "/api/v1/notes/"+reused.ID, "", "If-Match", `"1"`)
	e.create("eng/a.md", "a again", "alice")
}

func TestFolders(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"eng/runbooks/deploy.md", "eng/runbooks/rollback.md", "eng/design/api.md", "eng/readme.md", "ops/oncall.md", "top.md"} {
		e.create(p, "x", "alice")
	}
	gone, _ := e.create("ops/gone.md", "x", "alice")
	e.expect(http.StatusNoContent, http.MethodDelete, "/api/v1/notes/"+gone.ID, "", "If-Match", `"1"`)

	type folders struct {
		Prefix  string `json:"prefix"`
		Folders []struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			NoteCount int    `json:"noteCount"`
		} `json:"folders"`
	}
	get := func(prefix string) map[string]int {
		_, data := e.expect(http.StatusOK, http.MethodGet, "/api/v1/folders?prefix="+url.QueryEscape(prefix), "")
		got := decode[folders](t, data)
		out := map[string]int{}
		for _, f := range got.Folders {
			if !strings.HasSuffix(f.Path, "/") || !strings.HasSuffix(f.Path, f.Name+"/") {
				t.Errorf("folder %+v: path should end in name + slash", f)
			}
			out[f.Path] = f.NoteCount
		}
		return out
	}

	root := get("")
	if len(root) != 2 || root["eng/"] != 4 || root["ops/"] != 1 { // tombstone not counted
		t.Fatalf("root = %v", root)
	}
	eng := get("eng/")
	if len(eng) != 2 || eng["eng/runbooks/"] != 2 || eng["eng/design/"] != 1 {
		t.Fatalf("eng/ = %v", eng)
	}
	if leaf := get("eng/runbooks/"); len(leaf) != 0 {
		t.Fatalf("leaf folders = %v", leaf)
	}
}

func TestListPaginationAndFilters(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.create(fmt.Sprintf("eng/n%d.md", i), "common text", "alice", "team")
	}
	e.create("ops/zebra.md", "stripes everywhere", "alice", "animal")
	e.create("ops/plain.md", "nothing", "alice")
	gone, _ := e.create("ops/gone.md", "bye", "alice")
	e.expect(http.StatusNoContent, http.MethodDelete, "/api/v1/notes/"+gone.ID, "", "If-Match", `"1"`)

	// Page through everything live, 3 at a time.
	var seen []string
	cursor := ""
	pages := 0
	for {
		q := "limit=3"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		page := e.list(q)
		pages++
		if len(page.Items) > 3 {
			t.Fatalf("page %d has %d items", pages, len(page.Items))
		}
		for _, it := range page.Items {
			if it.Body != nil {
				t.Errorf("list item %s should omit body", it.Path)
			}
		}
		seen = append(seen, paths(page.Items)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
	}
	if pages != 3 || len(seen) != 7 {
		t.Fatalf("pages=%d seen=%v", pages, seen)
	}
	unique := map[string]bool{}
	for _, p := range seen {
		unique[p] = true
	}
	if len(unique) != 7 || unique["ops/gone.md"] {
		t.Fatalf("duplicates or tombstone in %v", seen)
	}

	if got := paths(e.list("prefix=eng/").Items); !sameSet(got, "eng/n0.md", "eng/n1.md", "eng/n2.md", "eng/n3.md", "eng/n4.md") {
		t.Errorf("prefix = %v", got)
	}
	if got := paths(e.list("tag=animal").Items); !sameSet(got, "ops/zebra.md") {
		t.Errorf("tag = %v", got)
	}
	if got := paths(e.list("q=zebra").Items); !sameSet(got, "ops/zebra.md") {
		t.Errorf("q = %v", got)
	}
	if got := paths(e.list("prefix=ops/&includeDeleted=true").Items); !sameSet(got, "ops/zebra.md", "ops/plain.md", "ops/gone.md") {
		t.Errorf("includeDeleted = %v", got)
	}

	withBody := e.list("tag=animal&fields=body").Items
	if len(withBody) != 1 || withBody[0].Body == nil || *withBody[0].Body != "stripes everywhere" {
		t.Errorf("fields=body = %+v", withBody)
	}
	e.expect(http.StatusBadRequest, http.MethodGet, "/api/v1/notes?cursor=garbage", "")
}

// wsClient is a minimal live-update client.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

type frame struct {
	Type string `json:"type"`
	Note *struct {
		ID           string `json:"id"`
		Path         string `json:"path"`
		Version      int64  `json:"version"`
		Deleted      bool   `json:"deleted"`
		PreviousPath string `json:"previousPath"`
	} `json:"note"`
	Presence *struct {
		NoteID string `json:"noteId"`
		Path   string `json:"path"`
		User   string `json:"user"`
		State  string `json:"state"`
	} `json:"presence"`
}

// dial connects and consumes the hello frame, so on return the subscription is live.
func (e *env) dial(query string) *wsClient {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/v1/ws?"+query, nil)
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	e.t.Cleanup(func() { conn.CloseNow() })
	c := &wsClient{t: e.t, conn: conn}
	if f := c.next(); f.Type != "hello" {
		e.t.Fatalf("first frame = %+v, want hello", f)
	}
	return c
}

func (c *wsClient) next() frame {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, c.conn, &f); err != nil {
		c.t.Fatalf("reading frame: %v", err)
	}
	return f
}

func (c *wsClient) send(v any) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c.conn, v); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func TestWebSocketEventsAndPresence(t *testing.T) {
	e := newEnv(t)
	watcher := e.dial("user=watcher&prefix=eng/")
	editor := e.dial("user=carol")

	n, _ := e.create("eng/live.md", "v1", "alice")
	f := watcher.next()
	if f.Type != "note.created" || f.Note.ID != n.ID || f.Note.Path != "eng/live.md" || f.Note.Version != 1 {
		t.Fatalf("created = %+v", f)
	}
	if f := editor.next(); f.Type != "note.created" {
		t.Fatalf("editor created = %+v", f)
	}

	// Presence from the editor reaches the watcher, not the editor itself.
	editor.send(map[string]string{"type": "editing", "noteId": n.ID})
	f = watcher.next()
	if f.Type != "presence" || f.Presence.User != "carol" || f.Presence.State != "editing" ||
		f.Presence.NoteID != n.ID || f.Presence.Path != "eng/live.md" {
		t.Fatalf("presence = %+v", f)
	}
	editor.send(map[string]string{"type": "stopped", "noteId": n.ID})
	if f := watcher.next(); f.Type != "presence" || f.Presence.State != "stopped" {
		t.Fatalf("stopped = %+v", f)
	}

	notePath := "/api/v1/notes/" + n.ID
	e.expect(http.StatusOK, http.MethodPut, notePath, noteBody("eng/live.md", "v2"), "If-Match", `"1"`)
	if f := watcher.next(); f.Type != "note.updated" || f.Note.Version != 2 || f.Note.PreviousPath != "" {
		t.Fatalf("updated = %+v", f)
	}
	// The editor's next frame is its own view of the update; it saw no presence echo.
	if f := editor.next(); f.Type != "note.updated" {
		t.Fatalf("editor should see update next (no presence echo), got %+v", f)
	}

	// Moving out of the watched prefix still tells the watcher.
	e.expect(http.StatusOK, http.MethodPut, notePath, noteBody("ops/live.md", "v3"), "If-Match", `"2"`)
	f = watcher.next()
	if f.Type != "note.updated" || f.Note.Path != "ops/live.md" || f.Note.PreviousPath != "eng/live.md" {
		t.Fatalf("move = %+v", f)
	}

	e.expect(http.StatusNoContent, http.MethodDelete, notePath, "", "If-Match", `"3"`)
	// The watcher no longer matches ops/, so it must not get the delete. A later
	// eng/ event proves ordering without sleeping.
	e.create("eng/after.md", "x", "alice")
	if f := watcher.next(); f.Type != "note.created" || f.Note.Path != "eng/after.md" {
		t.Fatalf("watcher got %+v, want only eng/after.md", f)
	}

	// The unfiltered editor saw the delete.
	for {
		f := editor.next()
		if f.Type == "note.deleted" {
			if !f.Note.Deleted || f.Note.Version != 4 {
				t.Fatalf("deleted = %+v", f)
			}
			break
		}
	}
}

func TestReconnectCatchUp(t *testing.T) {
	e := newEnv(t)
	before, _ := e.create("sync/before.md", "old", "alice")

	time.Sleep(20 * time.Millisecond)
	// Truncate to the millisecond the server stores, so the inclusive
	// updatedSince bound cannot land just after our own write.
	since := time.Now().UTC().Truncate(time.Millisecond)

	// The client is "disconnected": these changes happen with no socket open.
	created, _ := e.create("sync/created.md", "new", "bob")
	edited, _ := e.create("sync/edited.md", "v1", "bob")
	e.expect(http.StatusOK, http.MethodPut, "/api/v1/notes/"+edited.ID, noteBody("sync/edited.md", "v2"), "If-Match", `"1"`)
	doomed, _ := e.create("sync/doomed.md", "x", "bob")
	e.expect(http.StatusNoContent, http.MethodDelete, "/api/v1/notes/"+doomed.ID, "", "If-Match", `"1"`)

	q := "updatedSince=" + url.QueryEscape(since.Format(time.RFC3339Nano)) + "&includeDeleted=true"
	items := e.list(q).Items
	byID := map[string]noteJSON{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if len(items) != 3 {
		t.Fatalf("catch-up returned %v, want exactly the 3 changed notes", paths(items))
	}
	if _, ok := byID[before.ID]; ok {
		t.Error("catch-up included a note changed before the marker")
	}
	if byID[created.ID].Version != 1 || byID[edited.ID].Version != 2 {
		t.Errorf("versions: created=%d edited=%d", byID[created.ID].Version, byID[edited.ID].Version)
	}
	if tomb := byID[doomed.ID]; !tomb.Deleted || tomb.Version != 2 {
		t.Errorf("tombstone = %+v", tomb)
	}

	// Without includeDeleted the tombstone is hidden.
	if got := paths(e.list("updatedSince=" + url.QueryEscape(since.Format(time.RFC3339Nano))).Items); !sameSet(got, "sync/created.md", "sync/edited.md") {
		t.Errorf("live-only catch-up = %v", got)
	}
	e.expect(http.StatusBadRequest, http.MethodGet, "/api/v1/notes?updatedSince=yesterday", "")
}
