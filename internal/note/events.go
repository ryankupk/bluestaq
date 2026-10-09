package note

// ChangeType names what happened to a note.
type ChangeType string

const (
	ChangeCreated ChangeType = "note.created"
	ChangeUpdated ChangeType = "note.updated"
	ChangeDeleted ChangeType = "note.deleted"
)

// Change is one successful write, as published to live subscribers.
type Change struct {
	Type ChangeType
	Note Note
	// PreviousPath is the note's path before this write. It is set only when
	// an update changed the path, so subscribers watching the old location
	// learn that the note left it.
	PreviousPath string
}

// Publisher receives changes after they are durably stored. Implementations
// must not block: a slow subscriber must never stall a write.
type Publisher interface {
	Publish(Change)
}

// NopPublisher discards every change.
type NopPublisher struct{}

func (NopPublisher) Publish(Change) {}
