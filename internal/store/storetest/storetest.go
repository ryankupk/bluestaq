// Package storetest is the conformance suite every note.Repository must pass.
// Running one suite against memstore and mongostore is what keeps the fake
// from drifting away from the real store.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ryankupk/notes/internal/note"
)

var baseTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Run executes every conformance case, each against a fresh repository.
func Run(t *testing.T, newRepo func(t *testing.T) note.Repository) {
	cases := []struct {
		name string
		fn   func(t *testing.T, ctx context.Context, repo note.Repository)
	}{
		{"CreateAndGet", testCreateAndGet},
		{"GetMissing", testGetMissing},
		{"CreatePathTaken", testCreatePathTaken},
		{"PathReusableAfterDelete", testPathReuseAfterDelete},
		{"GetReturnsDeleted", testGetReturnsDeleted},
		{"UpdateHappyPath", testUpdate},
		{"UpdateVersionConflict", testUpdateConflict},
		{"UpdateNotFound", testUpdateNotFound},
		{"UpdateDeletedIsNotFound", testUpdateDeleted},
		{"UpdateRenameCollision", testUpdateRenameCollision},
		{"UpdateKeepingOwnPath", testUpdateOwnPath},
		{"DeleteCAS", testDeleteCAS},
		{"ListOrderAndHidesDeleted", testListOrder},
		{"ListPrefix", testListPrefix},
		{"ListPrefixIsLiteral", testListPrefixLiteral},
		{"ListTag", testListTag},
		{"ListText", testListText},
		{"ListUpdatedSinceInclusive", testListUpdatedSince},
		{"ListKeysetPagination", testListPagination},
		{"ListLimit", testListLimit},
		{"ListEmptyIsNonNil", testListEmpty},
		{"Folders", testFolders},
		{"ReturnedNotesDoNotAlias", testNoAliasing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, context.Background(), newRepo(t))
		})
	}
}

func mk(id, path string, offsetMs int) note.Note {
	at := baseTime.Add(time.Duration(offsetMs) * time.Millisecond)
	return note.Note{
		ID: id, Path: path, Body: "body of " + id, Tags: []string{}, Version: 1,
		CreatedAt: at, UpdatedAt: at, CreatedBy: "creator", UpdatedBy: "creator",
	}
}

func mustCreate(t *testing.T, ctx context.Context, repo note.Repository, notes ...note.Note) {
	t.Helper()
	for _, n := range notes {
		if err := repo.Create(ctx, n); err != nil {
			t.Fatalf("Create(%s): %v", n.ID, err)
		}
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func ids(notes []note.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	return out
}

func wantIDs(t *testing.T, got []note.Note, want ...string) {
	t.Helper()
	gotIDs := ids(got)
	if fmt.Sprint(gotIDs) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want %v", gotIDs, want)
	}
}

func list(t *testing.T, ctx context.Context, repo note.Repository, q note.ListQuery) []note.Note {
	t.Helper()
	got, err := repo.List(ctx, q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return got
}

func testCreateAndGet(t *testing.T, ctx context.Context, repo note.Repository) {
	n := mk("a", "eng/a.md", 0)
	n.Tags = []string{"x", "y"}
	mustCreate(t, ctx, repo, n)

	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != n.ID || got.Path != n.Path || got.Body != n.Body || got.Version != 1 ||
		got.Deleted || got.CreatedBy != "creator" || got.UpdatedBy != "creator" ||
		!got.CreatedAt.Equal(n.CreatedAt) || !got.UpdatedAt.Equal(n.UpdatedAt) ||
		fmt.Sprint(got.Tags) != "[x y]" {
		t.Fatalf("Get = %+v, want %+v", got, n)
	}
}

func testGetMissing(t *testing.T, ctx context.Context, repo note.Repository) {
	_, err := repo.Get(ctx, "nope")
	wantErr(t, err, note.ErrNotFound)
}

func testCreatePathTaken(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "p.md", 0))
	wantErr(t, repo.Create(ctx, mk("b", "p.md", 1)), note.ErrPathTaken)
	// Paths are case-sensitive.
	mustCreate(t, ctx, repo, mk("c", "P.md", 2))
}

func testPathReuseAfterDelete(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "p.md", 0))
	if _, err := repo.Delete(ctx, "a", 1, "bob", baseTime.Add(time.Second)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	mustCreate(t, ctx, repo, mk("b", "p.md", 2))

	// Two tombstones may share a path too.
	if _, err := repo.Delete(ctx, "b", 1, "bob", baseTime.Add(2*time.Second)); err != nil {
		t.Fatalf("Delete b: %v", err)
	}
	mustCreate(t, ctx, repo, mk("c", "p.md", 3))
}

func testGetReturnsDeleted(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "p.md", 0))
	at := baseTime.Add(time.Minute)
	deleted, err := repo.Delete(ctx, "a", 1, "bob", at)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted.Deleted || deleted.Version != 2 || deleted.UpdatedBy != "bob" || !deleted.UpdatedAt.Equal(at) || deleted.Path != "p.md" {
		t.Fatalf("Delete returned %+v", deleted)
	}
	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get deleted: %v", err)
	}
	if !got.Deleted || got.Version != 2 {
		t.Fatalf("Get = %+v, want deleted v2", got)
	}
}

func testUpdate(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "old.md", 0))
	at := baseTime.Add(time.Hour)
	got, err := repo.Update(ctx, note.Note{
		ID: "a", Path: "new/path.md", Body: "new body", Tags: []string{"t"},
		UpdatedAt: at, UpdatedBy: "alice",
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Path != "new/path.md" || got.Body != "new body" || fmt.Sprint(got.Tags) != "[t]" ||
		got.Version != 2 || !got.UpdatedAt.Equal(at) || got.UpdatedBy != "alice" ||
		got.CreatedBy != "creator" || !got.CreatedAt.Equal(baseTime) {
		t.Fatalf("Update = %+v", got)
	}
	stored, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Version != 2 || stored.Path != "new/path.md" {
		t.Fatalf("stored = %+v", stored)
	}
}

func testUpdateConflict(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "p.md", 0))
	upd := note.Note{ID: "a", Path: "p.md", Body: "x", UpdatedAt: baseTime, UpdatedBy: "u"}
	if _, err := repo.Update(ctx, upd, 1); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	// Stale (1) and future (5) versions both conflict, and neither changes the note.
	for _, expected := range []int64{1, 5} {
		_, err := repo.Update(ctx, upd, expected)
		wantErr(t, err, note.ErrVersionConflict)
	}
	_, err := repo.Delete(ctx, "a", 1, "u", baseTime)
	wantErr(t, err, note.ErrVersionConflict)
	if got, _ := repo.Get(ctx, "a"); got.Version != 2 || got.Deleted {
		t.Fatalf("note changed by failed writes: %+v", got)
	}
}

func testUpdateNotFound(t *testing.T, ctx context.Context, repo note.Repository) {
	_, err := repo.Update(ctx, note.Note{ID: "nope", Path: "p.md"}, 1)
	wantErr(t, err, note.ErrNotFound)
}

func testUpdateDeleted(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "p.md", 0))
	if _, err := repo.Delete(ctx, "a", 1, "u", baseTime); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := repo.Update(ctx, note.Note{ID: "a", Path: "p.md"}, 2)
	wantErr(t, err, note.ErrNotFound)
	_, err = repo.Delete(ctx, "a", 2, "u", baseTime)
	wantErr(t, err, note.ErrNotFound)
}

func testUpdateRenameCollision(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "a.md", 0), mk("b", "b.md", 1))
	_, err := repo.Update(ctx, note.Note{ID: "b", Path: "a.md", UpdatedAt: baseTime}, 1)
	wantErr(t, err, note.ErrPathTaken)
	if got, _ := repo.Get(ctx, "b"); got.Path != "b.md" || got.Version != 1 {
		t.Fatalf("failed rename changed note: %+v", got)
	}
}

func testUpdateOwnPath(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "a.md", 0))
	if _, err := repo.Update(ctx, note.Note{ID: "a", Path: "a.md", Body: "edited", UpdatedAt: baseTime}, 1); err != nil {
		t.Fatalf("Update with unchanged path: %v", err)
	}
}

func testDeleteCAS(t *testing.T, ctx context.Context, repo note.Repository) {
	_, err := repo.Delete(ctx, "nope", 1, "u", baseTime)
	wantErr(t, err, note.ErrNotFound)

	mustCreate(t, ctx, repo, mk("a", "a.md", 0))
	_, err = repo.Delete(ctx, "a", 9, "u", baseTime)
	wantErr(t, err, note.ErrVersionConflict)
	if _, err := repo.Delete(ctx, "a", 1, "u", baseTime); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func testListOrder(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("c", "c.md", 10), mk("a", "a.md", 20), mk("b", "b.md", 10), mk("gone", "gone.md", 5))
	if _, err := repo.Delete(ctx, "gone", 1, "u", baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Ties on updatedAt break by id.
	wantIDs(t, list(t, ctx, repo, note.ListQuery{}), "b", "c", "a")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{IncludeDeleted: true}), "b", "c", "a", "gone")
}

func testListPrefix(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("1", "eng/a.md", 1), mk("2", "eng/sub/b.md", 2), mk("3", "engineering/c.md", 3), mk("4", "ops/d.md", 4))
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Prefix: "eng/"}), "1", "2")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Prefix: "eng"}), "1", "2", "3")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Prefix: "eng/sub/"}), "2")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Prefix: "nothing/"}))
}

// Regex metacharacters in a prefix must match literally (mongostore builds a regex).
func testListPrefixLiteral(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("1", "a.b/x.md", 1), mk("2", "aXb/x.md", 2))
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Prefix: "a.b/"}), "1")
}

func testListTag(t *testing.T, ctx context.Context, repo note.Repository) {
	a, b, c := mk("1", "a.md", 1), mk("2", "b.md", 2), mk("3", "c.md", 3)
	a.Tags = []string{"go", "mongo"}
	b.Tags = []string{"go"}
	mustCreate(t, ctx, repo, a, b, c)
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Tag: "go"}), "1", "2")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Tag: "mongo"}), "1")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Tag: "rust"}))
}

func testListText(t *testing.T, ctx context.Context, repo note.Repository) {
	a, b, c := mk("1", "notes/one.md", 1), mk("2", "runbooks/deploy.md", 2), mk("3", "notes/three.md", 3)
	a.Body = "Kubernetes rollout checklist"
	b.Body = "restart the pods"
	c.Body = "nothing relevant"
	mustCreate(t, ctx, repo, a, b, c)
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Text: "kubernetes"}), "1") // case-insensitive, body
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Text: "runbooks"}), "2")   // path
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Text: "absent"}))
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Text: "kubernetes", Prefix: "runbooks/"})) // combines with other filters
}

func testListUpdatedSince(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("1", "a.md", 100), mk("2", "b.md", 200), mk("3", "c.md", 300))
	since := baseTime.Add(200 * time.Millisecond)
	wantIDs(t, list(t, ctx, repo, note.ListQuery{UpdatedSince: &since}), "2", "3")
}

func testListPagination(t *testing.T, ctx context.Context, repo note.Repository) {
	// Several notes share one updatedAt, so the id tiebreak is what keeps pages stable.
	mustCreate(t, ctx, repo,
		mk("a", "a.md", 100), mk("b", "b.md", 100), mk("c", "c.md", 100),
		mk("d", "d.md", 100), mk("e", "e.md", 200), mk("f", "f.md", 200), mk("g", "g.md", 300))

	var seen []string
	var cursor *note.Cursor
	for range 10 {
		page := list(t, ctx, repo, note.ListQuery{After: cursor, Limit: 3})
		if len(page) == 0 {
			break
		}
		seen = append(seen, ids(page)...)
		last := page[len(page)-1]
		cursor = &note.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	if fmt.Sprint(seen) != "[a b c d e f g]" {
		t.Fatalf("paged ids = %v, want [a b c d e f g] with no duplicates or gaps", seen)
	}
}

func testListLimit(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo, mk("a", "a.md", 1), mk("b", "b.md", 2), mk("c", "c.md", 3))
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Limit: 2}), "a", "b")
	wantIDs(t, list(t, ctx, repo, note.ListQuery{Limit: 0}), "a", "b", "c")
}

func testListEmpty(t *testing.T, ctx context.Context, repo note.Repository) {
	if got := list(t, ctx, repo, note.ListQuery{}); got == nil || len(got) != 0 {
		t.Fatalf("List on empty repo = %#v, want empty non-nil slice", got)
	}
}

func testFolders(t *testing.T, ctx context.Context, repo note.Repository) {
	mustCreate(t, ctx, repo,
		mk("1", "readme.md", 1),
		mk("2", "eng/a.md", 2),
		mk("3", "eng/runbooks/deploy.md", 3),
		mk("4", "eng/runbooks/oncall/x.md", 4),
		mk("5", "ops/b.md", 5),
		mk("6", "eng/design/d.md", 6),
		mk("7", "eng/gone/z.md", 7),
		mk("8", "engineering/e.md", 8))
	if _, err := repo.Delete(ctx, "7", 1, "u", baseTime); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	root, err := repo.Folders(ctx, "")
	if err != nil {
		t.Fatalf("Folders root: %v", err)
	}
	wantFolders(t, root, note.Folder{Path: "eng/", Name: "eng", NoteCount: 4},
		note.Folder{Path: "engineering/", Name: "engineering", NoteCount: 1},
		note.Folder{Path: "ops/", Name: "ops", NoteCount: 1})

	eng, err := repo.Folders(ctx, "eng/")
	if err != nil {
		t.Fatalf("Folders eng/: %v", err)
	}
	// "gone" is empty once its only note is deleted; eng/a.md is not a folder.
	wantFolders(t, eng, note.Folder{Path: "eng/design/", Name: "design", NoteCount: 1},
		note.Folder{Path: "eng/runbooks/", Name: "runbooks", NoteCount: 2})

	leaf, err := repo.Folders(ctx, "eng/design/")
	if err != nil {
		t.Fatalf("Folders leaf: %v", err)
	}
	wantFolders(t, leaf)
}

func wantFolders(t *testing.T, got []note.Folder, want ...note.Folder) {
	t.Helper()
	if got == nil {
		t.Fatal("Folders returned nil, want non-nil slice")
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("folders = %v, want %v", got, want)
	}
}

func testNoAliasing(t *testing.T, ctx context.Context, repo note.Repository) {
	n := mk("a", "a.md", 0)
	n.Tags = []string{"one"}
	mustCreate(t, ctx, repo, n)
	n.Tags[0] = "mutated-input"

	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Tags[0] = "mutated-output"

	again, _ := repo.Get(ctx, "a")
	if again.Tags[0] != "one" {
		t.Fatalf("stored tags = %v, want [one]", again.Tags)
	}
}
