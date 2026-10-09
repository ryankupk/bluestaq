package note

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultActor     = "anonymous"
	defaultListLimit = 50
	maxListLimit     = 200
)

// NoteInput is the client-controlled part of a note.
type NoteInput struct {
	Path string
	Body string
	Tags []string
}

// Page is one page of List results; Next is nil on the last page.
type Page struct {
	Items []Note
	Next  *Cursor
}

// ConflictError is returned on a stale expectedVersion. It carries the current
// note so the HTTP layer can hand it back for the client to diff and retry.
type ConflictError struct{ Current Note }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: current version is %d", ErrVersionConflict, e.Current.Version)
}

// Is makes errors.Is(err, ErrVersionConflict) true for a ConflictError.
func (e *ConflictError) Is(target error) bool { return target == ErrVersionConflict }

// Service applies validation and bookkeeping on top of a Repository.
type Service struct {
	repo  Repository
	pub   Publisher
	clock func() time.Time
	newID func() string
}

// NewService wires a Service. A nil pub, clock or newID gets a default.
func NewService(repo Repository, pub Publisher, clock func() time.Time, newID func() string) *Service {
	if pub == nil {
		pub = NopPublisher{}
	}
	if clock == nil {
		clock = time.Now
	}
	if newID == nil {
		newID = uuid.NewString
	}
	return &Service{repo: repo, pub: pub, clock: clock, newID: newID}
}

// now truncates to milliseconds because MongoDB stores nothing finer; doing it
// here keeps every store, and every cursor built from a timestamp, identical.
func (s *Service) now() time.Time { return s.clock().UTC().Truncate(time.Millisecond) }

func actorOrDefault(actor string) string {
	if actor = strings.TrimSpace(actor); actor != "" {
		return actor
	}
	return defaultActor
}

func normalizeInput(in NoteInput) (NoteInput, error) {
	p, err := NormalizePath(in.Path)
	if err != nil {
		return NoteInput{}, err
	}
	if err := validateBody(in.Body); err != nil {
		return NoteInput{}, err
	}
	tags, err := NormalizeTags(in.Tags)
	if err != nil {
		return NoteInput{}, err
	}
	return NoteInput{Path: p, Body: in.Body, Tags: tags}, nil
}

// Create stores a new note at version 1.
func (s *Service) Create(ctx context.Context, actor string, in NoteInput) (Note, error) {
	in, err := normalizeInput(in)
	if err != nil {
		return Note{}, err
	}
	actor = actorOrDefault(actor)
	at := s.now()
	n := Note{
		ID: s.newID(), Path: in.Path, Body: in.Body, Tags: in.Tags, Version: 1,
		CreatedAt: at, UpdatedAt: at, CreatedBy: actor, UpdatedBy: actor,
	}
	if err := s.repo.Create(ctx, n); err != nil {
		return Note{}, fmt.Errorf("creating note: %w", err)
	}
	s.pub.Publish(Change{Type: ChangeCreated, Note: n})
	return n, nil
}

// Get returns a live note; soft-deleted notes are reported as not found.
func (s *Service) Get(ctx context.Context, id string) (Note, error) {
	n, err := s.repo.Get(ctx, id)
	if err != nil {
		return Note{}, fmt.Errorf("getting note: %w", err)
	}
	if n.Deleted {
		return Note{}, fmt.Errorf("getting note: %w", ErrNotFound)
	}
	return n, nil
}

// Update replaces path, body and tags if the note is still at expectedVersion.
// A changed path renames or moves the note.
func (s *Service) Update(ctx context.Context, actor, id string, expectedVersion int64, in NoteInput) (Note, error) {
	in, err := normalizeInput(in)
	if err != nil {
		return Note{}, err
	}
	candidate := Note{
		ID: id, Path: in.Path, Body: in.Body, Tags: in.Tags,
		UpdatedAt: s.now(), UpdatedBy: actorOrDefault(actor),
	}
	updated, err := s.repo.Update(ctx, candidate, expectedVersion)
	if err != nil {
		return Note{}, s.wrapWrite(ctx, "updating note", id, err)
	}
	s.pub.Publish(Change{Type: ChangeUpdated, Note: updated})
	return updated, nil
}

// Delete soft-deletes the note if it is still at expectedVersion.
func (s *Service) Delete(ctx context.Context, actor, id string, expectedVersion int64) (Note, error) {
	deleted, err := s.repo.Delete(ctx, id, expectedVersion, actorOrDefault(actor), s.now())
	if err != nil {
		return Note{}, s.wrapWrite(ctx, "deleting note", id, err)
	}
	s.pub.Publish(Change{Type: ChangeDeleted, Note: deleted})
	return deleted, nil
}

// wrapWrite adds context to a failed write and, on a version conflict, swaps
// in a ConflictError carrying the current note.
func (s *Service) wrapWrite(ctx context.Context, action, id string, err error) error {
	if errors.Is(err, ErrVersionConflict) {
		current, getErr := s.repo.Get(ctx, id)
		if getErr == nil && !current.Deleted {
			return fmt.Errorf("%s: %w", action, &ConflictError{Current: current})
		}
		// The note vanished between the failed write and this read; report that instead.
		if getErr == nil || errors.Is(getErr, ErrNotFound) {
			return fmt.Errorf("%s: %w", action, ErrNotFound)
		}
		return fmt.Errorf("%s: loading current note: %w", action, getErr)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// List returns one page. It asks the repository for one extra row so Next is
// set only when more results really exist.
func (s *Service) List(ctx context.Context, q ListQuery) (Page, error) {
	switch {
	case q.Limit < 0:
		return Page{}, fmt.Errorf("%w: limit must not be negative", ErrInvalid)
	case q.Limit == 0:
		q.Limit = defaultListLimit
	case q.Limit > maxListLimit:
		q.Limit = maxListLimit
	}
	if q.Tag != "" {
		tag, err := normalizeTag(q.Tag)
		if err != nil {
			return Page{}, err
		}
		q.Tag = tag
	}
	q.Text = strings.TrimSpace(q.Text)

	limit := q.Limit
	q.Limit = limit + 1
	items, err := s.repo.List(ctx, q)
	if err != nil {
		return Page{}, fmt.Errorf("listing notes: %w", err)
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// Folders lists immediate child folders under prefix.
func (s *Service) Folders(ctx context.Context, prefix string) ([]Folder, error) {
	prefix, err := NormalizeFolderPrefix(prefix)
	if err != nil {
		return nil, err
	}
	folders, err := s.repo.Folders(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}
	return folders, nil
}
