package note_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/memstore"
)

type recordingPublisher struct {
	mu      sync.Mutex
	changes []note.Change
}

func (r *recordingPublisher) Publish(c note.Change) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, c)
}

func (r *recordingPublisher) types() []note.ChangeType {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []note.ChangeType
	for _, c := range r.changes {
		out = append(out, c.Type)
	}
	return out
}

type fixture struct {
	svc   *note.Service
	pub   *recordingPublisher
	now   time.Time
	idSeq int
}

func newFixture() *fixture {
	f := &fixture{
		pub: &recordingPublisher{},
		// Sub-millisecond part on purpose: the service must truncate it.
		now: time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.FixedZone("x", 3600)),
	}
	f.svc = note.NewService(memstore.New(), f.pub,
		func() time.Time { return f.now },
		func() string { f.idSeq++; return fmt.Sprintf("id-%d", f.idSeq) })
	return f
}

func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

func (f *fixture) create(t *testing.T, path string) note.Note {
	t.Helper()
	n, err := f.svc.Create(context.Background(), "alice", note.NoteInput{Path: path, Body: "b"})
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	return n
}

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	created, err := f.svc.Create(ctx, "alice", note.NoteInput{Path: " /eng//a.md/ ", Body: "hello", Tags: []string{"B", "a", "b"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wantTime := time.Date(2026, 3, 4, 4, 6, 7, 123000000, time.UTC)
	if created.ID != "id-1" || created.Path != "eng/a.md" || created.Version != 1 || created.Deleted ||
		created.CreatedBy != "alice" || created.UpdatedBy != "alice" ||
		!created.CreatedAt.Equal(wantTime) || !created.UpdatedAt.Equal(wantTime) ||
		created.CreatedAt.Location() != time.UTC ||
		fmt.Sprint(created.Tags) != "[a b]" {
		t.Fatalf("created = %+v", created)
	}

	got, err := f.svc.Get(ctx, created.ID)
	if err != nil || got.Path != "eng/a.md" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if got := f.pub.types(); fmt.Sprint(got) != "[note.created]" {
		t.Fatalf("published %v", got)
	}
}

func TestActorDefaulting(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	n, err := f.svc.Create(ctx, "  ", note.NoteInput{Path: "a.md"})
	if err != nil || n.CreatedBy != "anonymous" || n.UpdatedBy != "anonymous" {
		t.Fatalf("Create = %+v, %v", n, err)
	}
	n, err = f.svc.Update(ctx, "", n.ID, 1, note.NoteInput{Path: "a.md"})
	if err != nil || n.UpdatedBy != "anonymous" {
		t.Fatalf("Update = %+v, %v", n, err)
	}
	deleted, err := f.svc.Delete(ctx, "", n.ID, 2)
	if err != nil || deleted.UpdatedBy != "anonymous" {
		t.Fatalf("Delete = %+v, %v", deleted, err)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name string
		in   note.NoteInput
	}{
		{"empty path", note.NoteInput{Path: " / "}},
		{"dotdot", note.NoteInput{Path: "a/../b"}},
		{"too many tags", note.NoteInput{Path: "a", Tags: manyTags(33)}},
		{"body too large", note.NoteInput{Path: "a", Body: strings.Repeat("x", 1<<20+1)}},
		{"body invalid utf8", note.NoteInput{Path: "a", Body: "\xff"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			_, err := f.svc.Create(context.Background(), "u", tt.in)
			if !errors.Is(err, note.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if len(f.pub.types()) != 0 {
				t.Fatalf("published on failure: %v", f.pub.types())
			}
		})
	}

	t.Run("body at limit ok", func(t *testing.T) {
		f := newFixture()
		if _, err := f.svc.Create(context.Background(), "u", note.NoteInput{Path: "a", Body: strings.Repeat("x", 1<<20)}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func manyTags(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("t%d", i)
	}
	return out
}

func TestGetNotFoundAndDeleted(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if _, err := f.svc.Get(ctx, "nope"); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	n := f.create(t, "a.md")
	if _, err := f.svc.Delete(ctx, "alice", n.ID, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.svc.Get(ctx, n.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("Get deleted err = %v, want ErrNotFound", err)
	}
}

func TestUpdateAndRename(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	n := f.create(t, "old/a.md")
	f.advance(time.Minute)

	updated, err := f.svc.Update(ctx, "bob", n.ID, 1, note.NoteInput{Path: "/new//b.md", Body: "v2", Tags: []string{"Z"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Path != "new/b.md" || updated.Body != "v2" || updated.Version != 2 || updated.UpdatedBy != "bob" ||
		updated.CreatedBy != "alice" || !updated.UpdatedAt.After(updated.CreatedAt) || fmt.Sprint(updated.Tags) != "[z]" {
		t.Fatalf("updated = %+v", updated)
	}
	if got := f.pub.types(); fmt.Sprint(got) != "[note.created note.updated]" {
		t.Fatalf("published %v", got)
	}
	if last := f.pub.changes[1]; last.Note.Version != 2 || last.Note.Path != "new/b.md" {
		t.Fatalf("event note = %+v", last.Note)
	}
}

func TestUpdateErrors(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	a := f.create(t, "a.md")
	b := f.create(t, "b.md")
	published := len(f.pub.types())

	t.Run("path taken", func(t *testing.T) {
		_, err := f.svc.Update(ctx, "u", b.ID, 1, note.NoteInput{Path: "a.md"})
		if !errors.Is(err, note.ErrPathTaken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		_, err := f.svc.Update(ctx, "u", "nope", 1, note.NoteInput{Path: "x"})
		if !errors.Is(err, note.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		_, err := f.svc.Update(ctx, "u", a.ID, 1, note.NoteInput{Path: ".."})
		if !errors.Is(err, note.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("version conflict carries current note", func(t *testing.T) {
		if _, err := f.svc.Update(ctx, "u", a.ID, 1, note.NoteInput{Path: "a.md", Body: "second"}); err != nil {
			t.Fatalf("setup update: %v", err)
		}
		published++
		_, err := f.svc.Update(ctx, "u", a.ID, 1, note.NoteInput{Path: "a.md", Body: "stale"})
		if !errors.Is(err, note.ErrVersionConflict) {
			t.Fatalf("err = %v, want ErrVersionConflict", err)
		}
		var conflict *note.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *ConflictError", err)
		}
		if conflict.Current.ID != a.ID || conflict.Current.Version != 2 || conflict.Current.Body != "second" {
			t.Fatalf("current = %+v", conflict.Current)
		}
	})
	t.Run("delete conflict carries current note", func(t *testing.T) {
		_, err := f.svc.Delete(ctx, "u", a.ID, 1)
		var conflict *note.ConflictError
		if !errors.Is(err, note.ErrVersionConflict) || !errors.As(err, &conflict) || conflict.Current.Version != 2 {
			t.Fatalf("err = %v", err)
		}
	})

	if got := len(f.pub.types()); got != published {
		t.Fatalf("published %d events (%v), want only the successful ones (%d)", got, f.pub.types(), published)
	}
}

func TestDeleteAndPathReuse(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	n := f.create(t, "a.md")
	f.advance(time.Second)

	deleted, err := f.svc.Delete(ctx, "bob", n.ID, 1)
	if err != nil || !deleted.Deleted || deleted.Version != 2 || deleted.UpdatedBy != "bob" {
		t.Fatalf("Delete = %+v, %v", deleted, err)
	}
	if _, err := f.svc.Delete(ctx, "bob", n.ID, 2); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	// The freed path can be claimed by a new note.
	reused := f.create(t, "a.md")
	if reused.ID == n.ID {
		t.Fatal("reused path got the old id")
	}
	if got := f.pub.types(); fmt.Sprint(got) != "[note.created note.deleted note.created]" {
		t.Fatalf("published %v", got)
	}
}

func TestCreatePathTakenPublishesNothing(t *testing.T) {
	f := newFixture()
	f.create(t, "a.md")
	_, err := f.svc.Create(context.Background(), "u", note.NoteInput{Path: "/a.md"}) // normalizes to the same path
	if !errors.Is(err, note.ErrPathTaken) {
		t.Fatalf("err = %v", err)
	}
	if len(f.pub.types()) != 1 {
		t.Fatalf("published %v", f.pub.types())
	}
}

func TestListPaging(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	for i := range 5 {
		f.create(t, fmt.Sprintf("n%d.md", i))
		f.advance(time.Millisecond)
	}

	var seen []string
	var cursor *note.Cursor
	pages := 0
	for {
		page, err := f.svc.List(ctx, note.ListQuery{Limit: 2, After: cursor})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		pages++
		for _, n := range page.Items {
			seen = append(seen, n.Path)
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	if fmt.Sprint(seen) != "[n0.md n1.md n2.md n3.md n4.md]" || pages != 3 {
		t.Fatalf("seen %v in %d pages", seen, pages)
	}

	// An exactly-full last page must not advertise a next page.
	page, err := f.svc.List(ctx, note.ListQuery{Limit: 5})
	if err != nil || len(page.Items) != 5 || page.Next != nil {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestListLimitsAndFilters(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	for i := range 60 {
		f.create(t, fmt.Sprintf("n%02d.md", i))
	}
	page, err := f.svc.List(ctx, note.ListQuery{})
	if err != nil || len(page.Items) != 50 || page.Next == nil {
		t.Fatalf("default limit: %d items, next=%v, err=%v", len(page.Items), page.Next, err)
	}
	if _, err := f.svc.List(ctx, note.ListQuery{Limit: -1}); !errors.Is(err, note.ErrInvalid) {
		t.Fatalf("negative limit err = %v", err)
	}
	page, err = f.svc.List(ctx, note.ListQuery{Limit: 10_000})
	if err != nil || len(page.Items) != 60 || page.Next != nil {
		t.Fatalf("clamped limit: %d items, err=%v", len(page.Items), err)
	}

	tagged, err := f.svc.Create(ctx, "u", note.NoteInput{Path: "t.md", Tags: []string{"Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.svc.List(ctx, note.ListQuery{Tag: " ALPHA "}) // tag filter is normalized like stored tags
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != tagged.ID {
		t.Fatalf("tag filter: %+v, %v", page.Items, err)
	}
}

func TestFolders(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.create(t, "eng/runbooks/a.md")
	f.create(t, "eng/b.md")

	folders, err := f.svc.Folders(ctx, "/eng")
	if err != nil || len(folders) != 1 || folders[0].Path != "eng/runbooks/" || folders[0].NoteCount != 1 {
		t.Fatalf("folders = %+v, %v", folders, err)
	}
	if _, err := f.svc.Folders(ctx, "a/../b"); !errors.Is(err, note.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaults(t *testing.T) {
	svc := note.NewService(memstore.New(), nil, nil, nil)
	n, err := svc.Create(context.Background(), "", note.NoteInput{Path: "a.md"})
	if err != nil || len(n.ID) != 36 || n.CreatedAt.IsZero() {
		t.Fatalf("Create = %+v, %v", n, err)
	}
}

func TestConflictErrorMessage(t *testing.T) {
	err := &note.ConflictError{Current: note.Note{Version: 7}}
	if !errors.Is(err, note.ErrVersionConflict) || errors.Is(err, note.ErrNotFound) {
		t.Fatal("Is mismatch")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("message = %q", err.Error())
	}
}
