package httpapi

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ryankupk/notes/internal/note"
)

// noteResponse is the wire shape of a note. It is separate from note.Note so
// the API contract (title, optional body) can evolve independently of storage.
type noteResponse struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Title string `json:"title"`
	// Body is a pointer so list items can omit it while an empty body on a
	// single-note response still serializes as "".
	Body      *string   `json:"body,omitempty"`
	Tags      []string  `json:"tags"`
	Version   int64     `json:"version"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedBy string    `json:"createdBy"`
	UpdatedBy string    `json:"updatedBy"`
}

func newNoteResponse(n note.Note, withBody bool) noteResponse {
	r := noteResponse{
		ID: n.ID, Path: n.Path, Title: n.Title(), Tags: n.Tags, Version: n.Version,
		Deleted: n.Deleted, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		CreatedBy: n.CreatedBy, UpdatedBy: n.UpdatedBy,
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	if withBody {
		body := n.Body
		r.Body = &body
	}
	return r
}

// noteRequest is the body of POST and PUT.
type noteRequest struct {
	Path string   `json:"path"`
	Body string   `json:"body"`
	Tags []string `json:"tags"`
}

func (r noteRequest) input() note.NoteInput {
	return note.NoteInput{Path: r.Path, Body: r.Body, Tags: r.Tags}
}

type listResponse struct {
	Items      []noteResponse `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type folderResponse struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	NoteCount int    `json:"noteCount"`
}

type foldersResponse struct {
	Prefix  string           `json:"prefix"`
	Folders []folderResponse `json:"folders"`
}

// encodeCursor produces the opaque "<updatedAt>|<id>" token.
func encodeCursor(c note.Cursor) string {
	raw := c.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (note.Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return note.Cursor{}, fmt.Errorf("malformed cursor")
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return note.Cursor{}, fmt.Errorf("malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return note.Cursor{}, fmt.Errorf("malformed cursor")
	}
	return note.Cursor{UpdatedAt: at, ID: id}, nil
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch accepts a quoted ("3") or bare (3) integer version. Weak
// validators, lists and "*" are rejected: a version is an exact match.
func parseIfMatch(h string) (int64, error) {
	h = strings.TrimSpace(h)
	if len(h) >= 2 && h[0] == '"' && h[len(h)-1] == '"' {
		h = h[1 : len(h)-1]
	}
	v, err := strconv.ParseInt(h, 10, 64)
	if err != nil || v < 0 || strings.HasPrefix(h, "+") {
		return 0, fmt.Errorf("If-Match must be a note version such as \"3\"")
	}
	return v, nil
}

// matchesETag implements If-None-Match for a single current version.
func matchesETag(header string, version int64) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == "*" || candidate == etag(version) {
			return true
		}
	}
	return false
}
