// Package memstore is an in-memory note.Repository for fast tests. It is held
// to the same behavior as mongostore by the shared suite in storetest.
package memstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ryankupk/notes/internal/note"
)

// Store keeps notes in a map guarded by a mutex.
type Store struct {
	mu    sync.Mutex
	notes map[string]note.Note
}

var _ note.Repository = (*Store)(nil)

func New() *Store { return &Store{notes: make(map[string]note.Note)} }

// clone copies the Tags slice so callers can never alias stored state.
func clone(n note.Note) note.Note {
	n.Tags = slices.Clone(n.Tags)
	if n.Tags == nil {
		n.Tags = []string{}
	}
	return n
}

// pathTakenLocked reports whether a non-deleted note other than exceptID uses path.
func (s *Store) pathTakenLocked(path, exceptID string) bool {
	for id, existing := range s.notes {
		if id != exceptID && !existing.Deleted && existing.Path == path {
			return true
		}
	}
	return false
}

func (s *Store) Create(ctx context.Context, n note.Note) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.notes[n.ID]; exists {
		return fmt.Errorf("note id %q already exists", n.ID)
	}
	if !n.Deleted && s.pathTakenLocked(n.Path, n.ID) {
		return note.ErrPathTaken
	}
	s.notes[n.ID] = clone(n)
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (note.Note, error) {
	if err := ctx.Err(); err != nil {
		return note.Note{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	n, ok := s.notes[id]
	if !ok {
		return note.Note{}, note.ErrNotFound
	}
	return clone(n), nil
}

// liveForWriteLocked finds a note for a CAS write, mapping the failure cases.
func (s *Store) liveForWriteLocked(id string, expectedVersion int64) (note.Note, error) {
	current, ok := s.notes[id]
	if !ok || current.Deleted {
		return note.Note{}, note.ErrNotFound
	}
	if current.Version != expectedVersion {
		return note.Note{}, note.ErrVersionConflict
	}
	return current, nil
}

func (s *Store) Update(ctx context.Context, n note.Note, expectedVersion int64) (note.Note, error) {
	if err := ctx.Err(); err != nil {
		return note.Note{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.liveForWriteLocked(n.ID, expectedVersion)
	if err != nil {
		return note.Note{}, err
	}
	if s.pathTakenLocked(n.Path, n.ID) {
		return note.Note{}, note.ErrPathTaken
	}
	current.Path = n.Path
	current.Body = n.Body
	current.Tags = slices.Clone(n.Tags)
	current.UpdatedAt = n.UpdatedAt
	current.UpdatedBy = n.UpdatedBy
	current.Version++
	s.notes[n.ID] = current
	return clone(current), nil
}

func (s *Store) Delete(ctx context.Context, id string, expectedVersion int64, by string, at time.Time) (note.Note, error) {
	if err := ctx.Err(); err != nil {
		return note.Note{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.liveForWriteLocked(id, expectedVersion)
	if err != nil {
		return note.Note{}, err
	}
	current.Deleted = true
	current.UpdatedAt = at
	current.UpdatedBy = by
	current.Version++
	s.notes[id] = current
	return clone(current), nil
}

func (s *Store) List(ctx context.Context, q note.ListQuery) ([]note.Note, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	words := queryWords(q.Text)

	s.mu.Lock()
	var matched []note.Note
	for _, n := range s.notes {
		if matches(n, q, words) {
			matched = append(matched, clone(n))
		}
	}
	s.mu.Unlock()

	slices.SortFunc(matched, func(a, b note.Note) int {
		return cmp.Or(a.UpdatedAt.Compare(b.UpdatedAt), strings.Compare(a.ID, b.ID))
	})
	if q.Limit > 0 && len(matched) > q.Limit {
		matched = matched[:q.Limit]
	}
	if matched == nil {
		matched = []note.Note{}
	}
	return matched, nil
}

func matches(n note.Note, q note.ListQuery, words []string) bool {
	switch {
	case n.Deleted && !q.IncludeDeleted:
		return false
	case !strings.HasPrefix(n.Path, q.Prefix):
		return false
	case q.Tag != "" && !slices.Contains(n.Tags, q.Tag):
		return false
	case q.UpdatedSince != nil && n.UpdatedAt.Before(*q.UpdatedSince):
		return false
	case q.After != nil && !afterCursor(n, *q.After):
		return false
	case len(words) > 0 && !containsAnyWord(n, words):
		return false
	}
	return true
}

// afterCursor is the keyset condition: (updatedAt > c.updatedAt) OR
// (updatedAt == c.updatedAt AND id > c.id).
func afterCursor(n note.Note, c note.Cursor) bool {
	if n.UpdatedAt.After(c.UpdatedAt) {
		return true
	}
	return n.UpdatedAt.Equal(c.UpdatedAt) && n.ID > c.ID
}

// Text search approximation: MongoDB's $text index tokenizes path and body,
// matches any query word case-insensitively, and does no stemming here (the
// index is created with language "none"). Memstore mirrors that with a
// case-insensitive whole-word match of any query word against path+body, where
// words are runs of letters and digits. Exotic tokenization (hyphens,
// diacritics) may differ, so conformance tests use single plain words.
func queryWords(text string) []string { return tokenize(text) }

func containsAnyWord(n note.Note, words []string) bool {
	haystack := tokenize(n.Path + " " + n.Body)
	for _, word := range words {
		if slices.Contains(haystack, word) {
			return true
		}
	}
	return false
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func (s *Store) Folders(ctx context.Context, prefix string) ([]note.Folder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	counts := make(map[string]int)
	for _, n := range s.notes {
		rest, ok := strings.CutPrefix(n.Path, prefix)
		if n.Deleted || !ok {
			continue
		}
		// A note sitting directly in the prefix folder has no further slash and is not a folder.
		if name, _, nested := strings.Cut(rest, "/"); nested {
			counts[name]++
		}
	}
	s.mu.Unlock()

	folders := make([]note.Folder, 0, len(counts))
	for name, count := range counts {
		folders = append(folders, note.Folder{Path: prefix + name + "/", Name: name, NoteCount: count})
	}
	slices.SortFunc(folders, func(a, b note.Folder) int { return strings.Compare(a.Name, b.Name) })
	return folders, nil
}
