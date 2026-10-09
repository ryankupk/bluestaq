// Package note is the domain core: the Note type, its validation rules, the
// Repository contract storage backends implement, and the Service that applies
// the rules and publishes change events. It depends on neither MongoDB nor HTTP.
package note

import (
	"errors"
	"path"
	"time"
)

// Sentinel errors. Callers branch on them with errors.Is; producers wrap them
// with %w to add context (for example which field was invalid).
var (
	ErrNotFound        = errors.New("note not found")
	ErrVersionConflict = errors.New("version conflict")
	ErrPathTaken       = errors.New("path already in use")
	ErrInvalid         = errors.New("invalid input")
)

// Note is a markdown document addressed by a slash-separated path.
// ID never changes, so renaming a note is only a Path change.
type Note struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	Version   int64     `json:"version"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedBy string    `json:"createdBy"`
	UpdatedBy string    `json:"updatedBy"`
}

// Title is derived from the path rather than stored, so it can never disagree with it.
func (n Note) Title() string { return path.Base(n.Path) }
