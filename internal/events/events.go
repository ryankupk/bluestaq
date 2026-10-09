// Package events is the in-process pub/sub that fans note changes and presence
// messages out to live WebSocket subscribers. It knows nothing about HTTP or
// WebSockets: subscribers just receive Event values on a channel.
package events

import (
	"path"
	"strings"
	"time"

	"github.com/ryankupk/notes/internal/note"
)

// Event types that appear on the wire in the "type" field.
const (
	TypeNoteCreated = string(note.ChangeCreated)
	TypeNoteUpdated = string(note.ChangeUpdated)
	TypeNoteDeleted = string(note.ChangeDeleted)
	TypePresence    = "presence"
)

// Presence states.
const (
	StateEditing = "editing"
	StateStopped = "stopped"
)

// NoteSummary is a note without its body: clients refetch the body, which
// keeps fan-out cheap. It mirrors the list-item shape of the REST API.
type NoteSummary struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Version   int64     `json:"version"`
	Tags      []string  `json:"tags"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
	// PreviousPath is set only on a move, so subscribers of the old location
	// can drop the note from their view.
	PreviousPath string `json:"previousPath,omitempty"`
}

// Presence says that a user started or stopped editing a note. It is never stored.
type Presence struct {
	NoteID string    `json:"noteId"`
	Path   string    `json:"path,omitempty"`
	User   string    `json:"user"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
}

// Event is one frame pushed to subscribers. Exactly one of Note and Presence is set.
type Event struct {
	Type     string       `json:"type"`
	Note     *NoteSummary `json:"note,omitempty"`
	Presence *Presence    `json:"presence,omitempty"`
}

// matches reports whether a subscriber filtering on prefix should receive e.
// A moved note matches on either its new or its previous path.
func (e Event) matches(prefix string) bool {
	switch {
	case e.Note != nil:
		return strings.HasPrefix(e.Note.Path, prefix) ||
			(e.Note.PreviousPath != "" && strings.HasPrefix(e.Note.PreviousPath, prefix))
	case e.Presence != nil:
		return strings.HasPrefix(e.Presence.Path, prefix)
	}
	return strings.HasPrefix("", prefix)
}

// NewNoteEvent converts a domain change into its wire event.
func NewNoteEvent(c note.Change) Event {
	tags := c.Note.Tags
	if tags == nil {
		tags = []string{}
	}
	return Event{
		Type: string(c.Type),
		Note: &NoteSummary{
			ID: c.Note.ID, Path: c.Note.Path, Title: path.Base(c.Note.Path), Version: c.Note.Version,
			Tags: tags, Deleted: c.Note.Deleted, UpdatedAt: c.Note.UpdatedAt, UpdatedBy: c.Note.UpdatedBy,
			PreviousPath: c.PreviousPath,
		},
	}
}
