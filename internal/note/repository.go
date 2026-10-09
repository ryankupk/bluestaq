package note

import (
	"context"
	"time"
)

// Cursor is a keyset position in the (updatedAt, id) ordering.
type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

// ListQuery filters and pages List. Zero values mean "no filter".
type ListQuery struct {
	// Prefix is a raw, anchored string prefix on the path (not folder-aware).
	Prefix string
	Tag    string
	// Text matches words in path or body; see Repository.List for the semantics.
	Text string
	// UpdatedSince is inclusive (>=), so a client resuming from the last
	// updatedAt it saw re-receives that note and dedupes by (id, version).
	UpdatedSince   *time.Time
	IncludeDeleted bool
	// After resumes strictly after this position.
	After *Cursor
	// Limit <= 0 means unlimited at the repository level; Service always sets one.
	Limit int
}

// Folder is an immediate child folder under a prefix.
type Folder struct {
	// Path is the full folder path with a trailing slash, usable as the next prefix.
	Path string
	// Name is the last segment of Path, without slashes.
	Name string
	// NoteCount counts non-deleted notes anywhere beneath the folder.
	NoteCount int
}

// Repository is the storage contract. memstore and mongostore both implement it
// and are both verified by the same suite in storetest.
type Repository interface {
	// Create stores n as given. ErrPathTaken if a non-deleted note already has n.Path.
	Create(ctx context.Context, n Note) error

	// Get returns the note even if soft-deleted (the service decides visibility).
	// ErrNotFound if the id does not exist.
	Get(ctx context.Context, id string) (Note, error)

	// Update atomically replaces path, body, tags, updatedAt and updatedBy and
	// increments version, provided the stored note has version expectedVersion
	// and is not deleted. It returns the new document.
	// ErrNotFound: id missing or deleted. ErrVersionConflict: version differs.
	// ErrPathTaken: another non-deleted note has the new path.
	Update(ctx context.Context, n Note, expectedVersion int64) (Note, error)

	// Delete soft-deletes with the same compare-and-set rules as Update and
	// increments version. The tombstone keeps its path, which frees for reuse.
	Delete(ctx context.Context, id string, expectedVersion int64, by string, at time.Time) (Note, error)

	// List returns notes ordered by (updatedAt, id) ascending. Text is a
	// case-insensitive match of any query word against whole words in path or
	// body (MongoDB $text semantics; memstore approximates them).
	List(ctx context.Context, q ListQuery) ([]Note, error)

	// Folders returns the immediate child folders under prefix (a normalized
	// folder prefix, "" for root) among non-deleted notes, sorted by name.
	Folders(ctx context.Context, prefix string) ([]Folder, error)
}
