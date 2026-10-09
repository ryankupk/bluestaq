package events

import (
	"sync"

	"github.com/ryankupk/notes/internal/note"
)

// DefaultBuffer is the per-subscriber queue depth.
const DefaultBuffer = 64

// Hub fans events out to subscribers. It implements note.Publisher.
//
// Delivery never blocks the caller: a subscriber whose buffer is full is
// dropped (its channel is closed and Dropped reports true) instead of
// stalling writers. The consumer is expected to reconnect and catch up with
// GET /notes?updatedSince=...
type Hub struct {
	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	buffer int
	closed bool
}

var _ note.Publisher = (*Hub)(nil)

// NewHub creates a hub whose subscribers each buffer up to buffer events
// (DefaultBuffer if buffer <= 0).
func NewHub(buffer int) *Hub {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	return &Hub{subs: make(map[*Subscription]struct{}), buffer: buffer}
}

// Publish implements note.Publisher.
func (h *Hub) Publish(c note.Change) { h.Broadcast(nil, NewNoteEvent(c)) }

// Broadcast delivers ev to every subscriber whose prefix matches, except from
// (which may be nil). Used for presence so senders do not hear their own echo.
//
// The mutex is held for the whole fan-out. That is cheap because every send is
// non-blocking, and it makes channel close and send mutually exclusive, which
// is what keeps this race-free.
func (h *Hub) Broadcast(from *Subscription, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s == from || !ev.matches(s.prefix) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			s.dropped = true
			h.removeLocked(s)
		}
	}
}

// Subscribe registers a subscriber that receives events whose path starts
// with prefix ("" matches everything). On a closed hub it returns a
// subscription whose channel is already closed.
func (h *Hub) Subscribe(prefix string) *Subscription {
	s := &Subscription{hub: h, prefix: prefix, ch: make(chan Event, h.buffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(s.ch)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Close shuts the hub down: every subscription is closed and later Subscribe
// calls get an already-closed one. It is idempotent. Consumers tell this apart
// from a slow-consumer drop with Closed and Subscription.Dropped.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		h.removeLocked(s)
	}
}

// Closed reports whether Close has been called.
func (h *Hub) Closed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// Len reports the number of live subscribers.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// removeLocked unregisters s and closes its channel. Caller holds h.mu.
func (h *Hub) removeLocked(s *Subscription) {
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	close(s.ch)
}

// Subscription is one consumer's view of the hub.
type Subscription struct {
	hub    *Hub
	prefix string
	ch     chan Event
	// dropped is guarded by hub.mu.
	dropped bool
}

// C returns the event channel. It is closed when the subscription is closed or dropped.
func (s *Subscription) C() <-chan Event { return s.ch }

// Prefix returns the subscription's path prefix filter.
func (s *Subscription) Prefix() string { return s.prefix }

// Dropped reports whether the hub closed this subscription because it fell
// behind. Meaningful once C is closed.
func (s *Subscription) Dropped() bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.dropped
}

// Close unsubscribes. It is idempotent and safe to call concurrently with Broadcast.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}
