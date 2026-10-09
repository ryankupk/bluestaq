package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/ryankupk/notes/internal/note"
)

const userHeader = "X-User-ID"

// actor is the single place the caller's identity is read. Auth will replace
// its body; the service defaults an empty actor to "anonymous".
func actor(c echo.Context) string { return c.Request().Header.Get(userHeader) }

// wsActor also accepts ?user= because browsers cannot set headers on a
// WebSocket handshake. It is kept off REST routes so a URL can't override the
// header there.
func wsActor(c echo.Context) string {
	if u := c.QueryParam("user"); u != "" {
		return u
	}
	return actor(c)
}

func badRequest(format string, args ...any) error {
	return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf(format, args...))
}

// decodeNote strictly decodes a JSON body: unknown fields and trailing data are rejected.
func decodeNote(c echo.Context) (noteRequest, error) {
	var req noteRequest
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			return req, httpErr // e.g. 413 from the body limit middleware
		}
		return req, badRequest("invalid JSON body: %v", err)
	}
	if dec.More() {
		return req, badRequest("invalid JSON body: unexpected data after object")
	}
	return req, nil
}

// requireVersion reads the mandatory If-Match precondition.
func requireVersion(c echo.Context) (int64, error) {
	h := c.Request().Header.Get("If-Match")
	if strings.TrimSpace(h) == "" {
		return 0, echo.NewHTTPError(http.StatusPreconditionRequired, "If-Match header with the note version is required")
	}
	v, err := parseIfMatch(h)
	if err != nil {
		return 0, badRequest("%v", err)
	}
	return v, nil
}

func (h *handler) createNote(c echo.Context) error {
	req, err := decodeNote(c)
	if err != nil {
		return err
	}
	n, err := h.notes.Create(c.Request().Context(), actor(c), req.input())
	if err != nil {
		return err
	}
	c.Response().Header().Set("ETag", etag(n.Version))
	c.Response().Header().Set(echo.HeaderLocation, "/api/v1/notes/"+n.ID)
	return c.JSON(http.StatusCreated, newNoteResponse(n, true))
}

func (h *handler) getNote(c echo.Context) error {
	n, err := h.notes.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	tag := etag(n.Version)
	c.Response().Header().Set("ETag", tag)
	if inm := c.Request().Header.Get("If-None-Match"); inm != "" && matchesETag(inm, n.Version) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.JSON(http.StatusOK, newNoteResponse(n, true))
}

func (h *handler) updateNote(c echo.Context) error {
	version, err := requireVersion(c)
	if err != nil {
		return err
	}
	req, err := decodeNote(c)
	if err != nil {
		return err
	}
	n, err := h.notes.Update(c.Request().Context(), actor(c), c.Param("id"), version, req.input())
	if err != nil {
		return err
	}
	c.Response().Header().Set("ETag", etag(n.Version))
	return c.JSON(http.StatusOK, newNoteResponse(n, true))
}

func (h *handler) deleteNote(c echo.Context) error {
	version, err := requireVersion(c)
	if err != nil {
		return err
	}
	if _, err := h.notes.Delete(c.Request().Context(), actor(c), c.Param("id"), version); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handler) listNotes(c echo.Context) error {
	q := c.QueryParams()
	lq := note.ListQuery{Prefix: q.Get("prefix"), Tag: q.Get("tag"), Text: q.Get("q")}

	if s := q.Get("updatedSince"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s) // also accepts plain RFC3339
		if err != nil {
			return badRequest("updatedSince must be an RFC3339 timestamp")
		}
		lq.UpdatedSince = &t
	}
	if s := q.Get("includeDeleted"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return badRequest("includeDeleted must be a boolean")
		}
		lq.IncludeDeleted = b
	}
	if s := q.Get("limit"); s != "" {
		l, err := strconv.Atoi(s)
		if err != nil {
			return badRequest("limit must be an integer")
		}
		lq.Limit = l
	}
	if s := q.Get("cursor"); s != "" {
		cur, err := decodeCursor(s)
		if err != nil {
			return badRequest("%v", err)
		}
		lq.After = &cur
	}
	withBody := false
	if s := q.Get("fields"); s != "" {
		for _, f := range strings.Split(s, ",") {
			if strings.TrimSpace(f) != "body" {
				return badRequest("fields supports only \"body\"")
			}
			withBody = true
		}
	}

	page, err := h.notes.List(c.Request().Context(), lq)
	if err != nil {
		return err
	}
	resp := listResponse{Items: make([]noteResponse, 0, len(page.Items))}
	for _, n := range page.Items {
		resp.Items = append(resp.Items, newNoteResponse(n, withBody))
	}
	if page.Next != nil {
		resp.NextCursor = encodeCursor(*page.Next)
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *handler) listFolders(c echo.Context) error {
	folders, err := h.notes.Folders(c.Request().Context(), c.QueryParam("prefix"))
	if err != nil {
		return err
	}
	resp := foldersResponse{Folders: make([]folderResponse, 0, len(folders))}
	if prefix, err := note.NormalizeFolderPrefix(c.QueryParam("prefix")); err == nil {
		resp.Prefix = prefix
	}
	for _, f := range folders {
		resp.Folders = append(resp.Folders, folderResponse{Path: f.Path, Name: f.Name, NoteCount: f.NoteCount})
	}
	return c.JSON(http.StatusOK, resp)
}
