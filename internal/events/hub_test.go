package events

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ryankupk/notes/internal/note"
)

func change(typ note.ChangeType, path string, version int64) note.Change {
	return note.Change{Type: typ, Note: note.Note{ID: "id-" + path, Path: path, Version: version}}
}

func recv(t *testing.T, s *Subscription) Event {
	t.Helper()
	select {
	case ev, ok := <-s.C():
		if !ok {
			t.Fatal("channel closed, want event")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
	return Event{}
}

func expectNone(t *testing.T, s *Subscription) {
	t.Helper()
	select {
	case ev, ok := <-s.C():
		t.Fatalf("unexpected receive: %+v ok=%v", ev, ok)
	default:
	}
}

func TestFanOut(t *testing.T) {
	h := NewHub(0)
	a, b := h.Subscribe(""), h.Subscribe("")
	h.Publish(change(note.ChangeCreated, "eng/a.md", 1))
	for _, s := range []*Subscription{a, b} {
		ev := recv(t, s)
		if ev.Type != "note.created" || ev.Note == nil || ev.Note.Path != "eng/a.md" || ev.Note.Title != "a.md" || ev.Note.Tags == nil {
			t.Errorf("event = %+v", ev)
		}
	}
}

func TestPrefixFilter(t *testing.T) {
	h := NewHub(0)
	eng, ops := h.Subscribe("eng/"), h.Subscribe("ops/")
	h.Publish(change(note.ChangeUpdated, "eng/a.md", 2))
	if ev := recv(t, eng); ev.Note.Path != "eng/a.md" {
		t.Errorf("got %+v", ev)
	}
	expectNone(t, ops)

	// Presence is filtered by its path too.
	h.Broadcast(nil, Event{Type: TypePresence, Presence: &Presence{NoteID: "x", Path: "ops/x.md"}})
	if ev := recv(t, ops); ev.Presence == nil {
		t.Errorf("got %+v", ev)
	}
	expectNone(t, eng)
}

func TestMovedNoteMatchesOldAndNewPrefix(t *testing.T) {
	h := NewHub(0)
	eng, ops, other := h.Subscribe("eng/"), h.Subscribe("ops/"), h.Subscribe("hr/")
	c := change(note.ChangeUpdated, "ops/a.md", 2)
	c.PreviousPath = "eng/a.md"
	h.Publish(c)
	for _, s := range []*Subscription{eng, ops} {
		ev := recv(t, s)
		if ev.Note.Path != "ops/a.md" || ev.Note.PreviousPath != "eng/a.md" {
			t.Errorf("event = %+v", ev.Note)
		}
	}
	expectNone(t, other)
}

func TestHubClose(t *testing.T) {
	h := NewHub(0)
	a, b := h.Subscribe(""), h.Subscribe("eng/")
	h.Close()
	h.Close() // idempotent
	if !h.Closed() || h.Len() != 0 {
		t.Fatalf("Closed=%v Len=%d", h.Closed(), h.Len())
	}
	for _, s := range []*Subscription{a, b} {
		if _, ok := <-s.C(); ok {
			t.Error("channel not closed")
		}
		if s.Dropped() {
			t.Error("closed by hub must not look like a slow-consumer drop")
		}
		s.Close() // safe after hub close
	}

	late := h.Subscribe("")
	if _, ok := <-late.C(); ok {
		t.Error("subscription on closed hub should be closed")
	}
	h.Publish(change(note.ChangeCreated, "x", 1)) // must not panic
}

func TestBroadcastExcludesSender(t *testing.T) {
	h := NewHub(0)
	a, b := h.Subscribe(""), h.Subscribe("")
	h.Broadcast(a, Event{Type: TypePresence, Presence: &Presence{Path: "p"}})
	recv(t, b)
	expectNone(t, a)
}

func TestUnsubscribe(t *testing.T) {
	h := NewHub(0)
	s := h.Subscribe("")
	s.Close()
	s.Close() // idempotent
	if _, ok := <-s.C(); ok {
		t.Error("channel still open after Close")
	}
	if s.Dropped() {
		t.Error("explicit Close must not count as dropped")
	}
	if h.Len() != 0 {
		t.Errorf("Len = %d", h.Len())
	}
	h.Publish(change(note.ChangeCreated, "a", 1)) // must not panic on closed channel
}

func TestSlowConsumerDropped(t *testing.T) {
	h := NewHub(2)
	slow, fast := h.Subscribe(""), h.Subscribe("")
	for i := range 3 {
		h.Publish(change(note.ChangeUpdated, "a", int64(i)))
		recv(t, fast)
	}
	if !slow.Dropped() {
		t.Fatal("slow subscriber not dropped")
	}
	// Buffered events are still readable, then the channel reports closed.
	recv(t, slow)
	recv(t, slow)
	if _, ok := <-slow.C(); ok {
		t.Error("slow channel not closed")
	}
	if h.Len() != 1 {
		t.Errorf("Len = %d, want 1", h.Len())
	}
	h.Publish(change(note.ChangeUpdated, "a", 9))
	recv(t, fast)
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub(4) // small, so drops race with Close too
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 200 {
				h.Publish(change(note.ChangeUpdated, fmt.Sprintf("eng/%d-%d", i, j), 1))
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				s := h.Subscribe("eng/")
				select {
				case <-s.C():
				default:
				}
				s.Close()
				s.Dropped()
			}
		}()
	}
	wg.Wait()
	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}
}
