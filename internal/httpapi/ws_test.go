package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ryankupk/notes/internal/events"
	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/memstore"
)

type wsEnv struct {
	t   *testing.T
	srv *httptest.Server
	hub *events.Hub
}

func newWSEnv(t *testing.T) *wsEnv {
	t.Helper()
	hub := events.NewHub(0)
	svc := note.NewService(memstore.New(), hub, nil, nil)
	srv := httptest.NewServer(NewRouter(Deps{
		Pinger: fakePinger{}, Notes: svc, Hub: hub,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(srv.Close)
	return &wsEnv{t: t, srv: srv, hub: hub}
}

// frame is a loosely typed view of any server frame.
type frame struct {
	Type   string `json:"type"`
	User   string `json:"user"`
	Prefix string `json:"prefix"`
	Note   *struct {
		ID      string   `json:"id"`
		Path    string   `json:"path"`
		Title   string   `json:"title"`
		Version int64    `json:"version"`
		Tags    []string `json:"tags"`
		Deleted bool     `json:"deleted"`
		By      string   `json:"updatedBy"`
	} `json:"note"`
	Presence *struct {
		NoteID string `json:"noteId"`
		Path   string `json:"path"`
		User   string `json:"user"`
		State  string `json:"state"`
		At     string `json:"at"`
	} `json:"presence"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

// dial connects and consumes the hello frame, so on return the subscription is live.
func (e *wsEnv) dial(query string) *wsClient {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws?" + query
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	c := &wsClient{t: e.t, conn: conn}
	e.t.Cleanup(func() { conn.CloseNow() })
	if f := c.next(); f.Type != "hello" {
		e.t.Fatalf("first frame = %+v, want hello", f)
	}
	return c
}

func (c *wsClient) next() frame {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, c.conn, &f); err != nil {
		c.t.Fatalf("reading frame: %v", err)
	}
	return f
}

// expectSilence asserts nothing arrives shortly; callers use it only after a
// positive sync event, so it guards against extra frames, not ordering.
func (c *wsClient) expectSilence() {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, c.conn, &f); err == nil {
		c.t.Fatalf("unexpected frame %+v", f)
	}
	// A timed-out read closes the connection, so the client is unusable after this.
}

func (c *wsClient) send(v any) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c.conn, v); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func (e *wsEnv) rest(method, path, body string, hdr ...string) (int, map[string]any) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out) // 204 has no body
	return resp.StatusCode, out
}

func (e *wsEnv) create(path, user string) (id string, version int64) {
	e.t.Helper()
	status, out := e.rest(http.MethodPost, "/notes", fmt.Sprintf(`{"path":%q,"body":"x"}`, path), "X-User-ID", user)
	if status != http.StatusCreated {
		e.t.Fatalf("create status = %d", status)
	}
	return out["id"].(string), int64(out["version"].(float64))
}

func TestWSHello(t *testing.T) {
	e := newWSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/v1/ws?prefix=eng&user=alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var f frame
	if err := wsjson.Read(ctx, conn, &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != "hello" || f.User != "alice" || f.Prefix != "eng/" {
		t.Errorf("hello = %+v", f)
	}
}

func TestWSDefaultsAndHeaderIdentity(t *testing.T) {
	e := newWSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws"

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var f frame
	if err := wsjson.Read(ctx, conn, &f); err != nil || f.User != "anonymous" {
		t.Errorf("anonymous: %+v %v", f, err)
	}

	conn2, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"X-User-ID": {"bob"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.CloseNow()
	f = frame{}
	if err := wsjson.Read(ctx, conn2, &f); err != nil || f.User != "bob" {
		t.Errorf("header identity: %+v %v", f, err)
	}
}

func TestWSInvalidPrefixRejected(t *testing.T) {
	e := newWSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/v1/ws?prefix=../x", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("err=%v resp=%v, want 400", err, resp)
	}
}

func TestWSNoteLifecycleEvents(t *testing.T) {
	e := newWSEnv(t)
	c := e.dial("")

	id, v := e.create("eng/a.md", "alice")
	f := c.next()
	if f.Type != "note.created" || f.Note == nil || f.Note.ID != id || f.Note.Path != "eng/a.md" ||
		f.Note.Title != "a.md" || f.Note.Version != 1 || f.Note.By != "alice" || f.Note.Tags == nil {
		t.Fatalf("created = %+v", f)
	}

	status, _ := e.rest(http.MethodPut, "/notes/"+id, `{"path":"eng/a.md","body":"y"}`, "If-Match", fmt.Sprint(v), "X-User-ID", "bob")
	if status != http.StatusOK {
		t.Fatalf("update status %d", status)
	}
	f = c.next()
	if f.Type != "note.updated" || f.Note.Version != 2 || f.Note.By != "bob" {
		t.Fatalf("updated = %+v", f)
	}

	status, _ = e.rest(http.MethodDelete, "/notes/"+id, "", "If-Match", "2")
	if status != http.StatusNoContent {
		t.Fatalf("delete status %d", status)
	}
	f = c.next()
	if f.Type != "note.deleted" || !f.Note.Deleted || f.Note.Version != 3 {
		t.Fatalf("deleted = %+v", f)
	}
}

func TestWSPrefixFilter(t *testing.T) {
	e := newWSEnv(t)
	eng := e.dial("prefix=eng/")
	all := e.dial("")

	e.create("ops/x.md", "a")
	e.create("eng/y.md", "a")

	// eng must see only eng/y.md; all sees both, in order.
	if f := eng.next(); f.Note.Path != "eng/y.md" {
		t.Errorf("eng got %+v", f.Note)
	}
	if f := all.next(); f.Note.Path != "ops/x.md" {
		t.Errorf("all first got %+v", f.Note)
	}
	if f := all.next(); f.Note.Path != "eng/y.md" {
		t.Errorf("all second got %+v", f.Note)
	}
	eng.expectSilence()
}

func TestWSPresenceRelayedToOthersNotSender(t *testing.T) {
	e := newWSEnv(t)
	id, _ := e.create("eng/doc.md", "x")
	alice := e.dial("user=alice")
	bob := e.dial("user=bob")
	other := e.dial("prefix=ops/&user=carol")

	alice.send(map[string]string{"type": "editing", "noteId": id})
	f := bob.next()
	if f.Type != "presence" || f.Presence == nil || f.Presence.NoteID != id || f.Presence.Path != "eng/doc.md" ||
		f.Presence.User != "alice" || f.Presence.State != "editing" || f.Presence.At == "" {
		t.Fatalf("presence = %+v", f.Presence)
	}

	alice.send(map[string]string{"type": "stopped", "noteId": id})
	if f := bob.next(); f.Presence.State != "stopped" {
		t.Fatalf("got %+v", f.Presence)
	}

	other.expectSilence() // prefix filter applies to presence
	alice.expectSilence() // sender never hears itself
}

func TestWSDisconnectEmitsStopped(t *testing.T) {
	e := newWSEnv(t)
	id1, _ := e.create("eng/1.md", "x")
	id2, _ := e.create("eng/2.md", "x")
	alice := e.dial("user=alice")
	bob := e.dial("user=bob")

	alice.send(map[string]string{"type": "editing", "noteId": id1})
	alice.send(map[string]string{"type": "editing", "noteId": id2})
	alice.send(map[string]string{"type": "stopped", "noteId": id2})
	for range 3 {
		bob.next()
	}

	if err := alice.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Logf("close: %v", err)
	}
	f := bob.next()
	if f.Presence == nil || f.Presence.NoteID != id1 || f.Presence.State != "stopped" || f.Presence.User != "alice" {
		t.Fatalf("got %+v", f.Presence)
	}
	bob.expectSilence() // id2 was already stopped explicitly
}

func TestWSInvalidMessagesKeepConnectionOpen(t *testing.T) {
	e := newWSEnv(t)
	c := e.dial("")

	cases := []struct {
		name string
		raw  string
		code string
	}{
		{"not json", `nope`, "invalid_message"},
		{"unknown type", `{"type":"dance"}`, "invalid_message"},
		{"missing noteId", `{"type":"editing"}`, "invalid_message"},
		{"unknown note", `{"type":"editing","noteId":"does-not-exist"}`, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := c.conn.Write(ctx, websocket.MessageText, []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			f := c.next()
			if f.Type != "error" || f.Error == nil || f.Error.Code != tc.code {
				t.Fatalf("frame = %+v", f)
			}
		})
	}

	// Still alive and subscribed after all the bad input.
	e.create("eng/ok.md", "x")
	if f := c.next(); f.Type != "note.created" {
		t.Fatalf("after errors got %+v", f)
	}
}

func TestWSOversizedMessageClosesConnection(t *testing.T) {
	e := newWSEnv(t)
	c := e.dial("")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"editing","noteId":"`+strings.Repeat("a", 8<<10)+`"}`))
	_, _, err := c.conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("err = %v, want close 1009", err)
	}
}

func TestWSNoGoroutineLeaks(t *testing.T) {
	e := newWSEnv(t)
	http.DefaultClient.CloseIdleConnections()
	runtime.GC()
	before := runtime.NumGoroutine()

	for range 5 {
		c := e.dial("")
		e.create("eng/leak.md"+fmt.Sprint(time.Now().UnixNano()), "x")
		c.next()
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}

	deadline := time.Now().Add(3 * time.Second)
	for e.hub.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.hub.Len(); n != 0 {
		t.Fatalf("hub still has %d subscribers", n)
	}
	http.DefaultClient.CloseIdleConnections()
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d -> %d\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}
