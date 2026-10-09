package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"

	"github.com/ryankupk/notes/internal/events"
	"github.com/ryankupk/notes/internal/note"
)

const (
	anonymousUser = "anonymous"

	// Client frames are tiny ({"type":..,"noteId":..}); anything bigger is abuse.
	wsReadLimit     = 4 << 10
	wsWriteTimeout  = 10 * time.Second
	wsPingInterval  = 30 * time.Second
	wsPingTimeout   = 10 * time.Second
	wsLookupTimeout = 5 * time.Second
)

// Frames that only exist on the WebSocket (note and presence events come from
// the events package).
type helloFrame struct {
	Type   string `json:"type"`
	User   string `json:"user"`
	Prefix string `json:"prefix"`
}

type errorFrame struct {
	Type  string      `json:"type"`
	Error errorDetail `json:"error"`
}

// clientMessage is what clients send: presence only.
type clientMessage struct {
	Type   string `json:"type"`
	NoteID string `json:"noteId"`
}

// wsConn is one accepted connection and the state its goroutines share.
type wsConn struct {
	conn   *websocket.Conn
	hub    *events.Hub
	sub    *events.Subscription
	notes  *note.Service
	user   string
	logger *slog.Logger
	// editing maps noteID -> path for notes this connection marked as being
	// edited. Only the reader goroutine touches it.
	editing map[string]string
}

// watch upgrades to a WebSocket and streams events until either side goes away.
//
// Goroutines: the handler goroutine is the reader; one writer drains the
// subscription and one pinger keeps the connection alive. All are bound to ctx,
// which the reader cancels on exit, and the handler waits for both before
// returning, so nothing outlives the connection.
func (h *handler) watch(c echo.Context) error {
	prefix, err := note.NormalizeFolderPrefix(c.QueryParam("prefix"))
	if err != nil {
		return badRequest("invalid prefix: %v", validationMessage(err))
	}
	user := wsActor(c)
	if user == "" {
		user = anonymousUser
	}

	// Origin checks (CORS) are deferred together with auth; accept any origin for now.
	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		// Accept has already written the HTTP error response.
		return nil
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(c.Request().Context())
	defer cancel()

	// Subscribe before hello so receiving hello guarantees no later event is missed.
	sub := h.hub.Subscribe(prefix)
	defer sub.Close()

	w := &wsConn{
		conn: conn, hub: h.hub, sub: sub, notes: h.notes, user: user,
		logger: h.logger, editing: make(map[string]string),
	}
	if err := w.write(ctx, helloFrame{Type: "hello", User: user, Prefix: prefix}); err != nil {
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.writeLoop(ctx, cancel) }()
	go func() { defer wg.Done(); w.pingLoop(ctx, cancel) }()

	w.readLoop(ctx)
	cancel()
	wg.Wait()

	// Clear stale presence for anything this connection left marked as editing.
	for id, p := range w.editing {
		w.hub.Broadcast(sub, w.presence(id, p, events.StateStopped))
	}
	return nil
}

func (w *wsConn) write(ctx context.Context, v any) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, w.conn, v)
}

func (w *wsConn) writeLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.sub.C():
			if !ok {
				if w.sub.Dropped() {
					// Slow consumer: tell it to reconnect and catch up via updatedSince.
					_ = w.conn.Close(websocket.StatusTryAgainLater, "too slow; reconnect and resync")
				}
				return
			}
			if err := w.write(ctx, ev); err != nil {
				return
			}
		}
	}
}

func (w *wsConn) pingLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, wsPingTimeout)
			err := w.conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// readLoop handles client messages until the connection closes. Malformed
// messages get an error frame and the connection stays open; only transport
// errors (including exceeding the read limit) end the loop.
func (w *wsConn) readLoop(ctx context.Context) {
	for {
		typ, data, err := w.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			w.sendError(ctx, "invalid_message", "messages must be JSON text frames")
			continue
		}
		var msg clientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			w.sendError(ctx, "invalid_message", "message is not valid JSON")
			continue
		}
		w.handleMessage(ctx, msg)
	}
}

func (w *wsConn) handleMessage(ctx context.Context, msg clientMessage) {
	state := ""
	switch msg.Type {
	case events.StateEditing, events.StateStopped:
		state = msg.Type
	default:
		w.sendError(ctx, "invalid_message", `type must be "editing" or "stopped"`)
		return
	}
	if msg.NoteID == "" {
		w.sendError(ctx, "invalid_message", "noteId is required")
		return
	}

	// The path comes from the service, not the client, so prefix filters apply
	// to presence and clients cannot spoof it. Unknown or deleted notes get an
	// error frame to the sender only; nothing is broadcast.
	lctx, cancel := context.WithTimeout(ctx, wsLookupTimeout)
	n, err := w.notes.Get(lctx, msg.NoteID)
	cancel()
	switch {
	case errors.Is(err, note.ErrNotFound):
		w.sendError(ctx, "not_found", "note not found")
		return
	case err != nil:
		if ctx.Err() == nil {
			w.logger.Error("presence lookup failed", "error", err, "note_id", msg.NoteID)
			w.sendError(ctx, "internal", "internal server error")
		}
		return
	}

	if state == events.StateEditing {
		w.editing[n.ID] = n.Path
	} else {
		delete(w.editing, n.ID)
	}
	w.hub.Broadcast(w.sub, w.presence(n.ID, n.Path, state))
}

func (w *wsConn) presence(noteID, path, state string) events.Event {
	return events.Event{Type: events.TypePresence, Presence: &events.Presence{
		NoteID: noteID, Path: path, User: w.user, State: state, At: time.Now().UTC(),
	}}
}

func (w *wsConn) sendError(ctx context.Context, code, message string) {
	if err := w.write(ctx, errorFrame{Type: "error", Error: errorDetail{Code: code, Message: message}}); err != nil {
		w.logger.Debug("writing error frame", "error", err)
	}
}
