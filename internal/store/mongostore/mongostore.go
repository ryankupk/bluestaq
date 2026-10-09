// Package mongostore is the MongoDB note.Repository. Every write is a single
// atomic document operation, so it needs neither transactions nor a replica set.
package mongostore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ryankupk/notes/internal/note"
)

// Store implements note.Repository on one collection.
type Store struct {
	coll *mongo.Collection
}

var _ note.Repository = (*Store)(nil)

func New(coll *mongo.Collection) *Store { return &Store{coll: coll} }

// document is the stored shape. It lives here, not on note.Note, to keep bson
// out of the domain package.
type document struct {
	ID        string    `bson:"_id"`
	Path      string    `bson:"path"`
	Body      string    `bson:"body"`
	Tags      []string  `bson:"tags"`
	Version   int64     `bson:"version"`
	Deleted   bool      `bson:"deleted"`
	CreatedAt time.Time `bson:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
	CreatedBy string    `bson:"createdBy"`
	UpdatedBy string    `bson:"updatedBy"`
}

func toDocument(n note.Note) document {
	tags := n.Tags
	if tags == nil {
		tags = []string{} // store [] rather than null so tag queries and clients see an array
	}
	return document{
		ID: n.ID, Path: n.Path, Body: n.Body, Tags: tags, Version: n.Version, Deleted: n.Deleted,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, CreatedBy: n.CreatedBy, UpdatedBy: n.UpdatedBy,
	}
}

func (d document) toNote() note.Note {
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	return note.Note{
		ID: d.ID, Path: d.Path, Body: d.Body, Tags: tags, Version: d.Version, Deleted: d.Deleted,
		// The driver may hand back local time; the API contract is UTC.
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
		CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy,
	}
}

// EnsureIndexes creates the indexes the queries rely on. It is idempotent, so
// it is safe to call on every startup.
func (s *Store) EnsureIndexes(ctx context.Context) error {
	models := []mongo.IndexModel{
		{
			// Unique among live notes only, so a deleted note's path can be reused.
			Keys: bson.D{{Key: "path", Value: 1}},
			Options: options.Index().SetName("path_live_unique").SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "deleted", Value: false}}),
		},
		{
			// Backs the (updatedAt, _id) sort, keyset cursors and updatedSince.
			Keys:    bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}},
			Options: options.Index().SetName("updatedAt_id"),
		},
		{
			Keys:    bson.D{{Key: "tags", Value: 1}},
			Options: options.Index().SetName("tags"),
		},
		{
			// Language "none" turns off stemming and stop words so matching is
			// plain case-insensitive whole words, which memstore can mirror.
			Keys:    bson.D{{Key: "path", Value: "text"}, {Key: "body", Value: "text"}},
			Options: options.Index().SetName("path_body_text").SetDefaultLanguage("none"),
		},
	}
	if _, err := s.coll.Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("creating indexes: %w", err)
	}
	return nil
}

func (s *Store) Create(ctx context.Context, n note.Note) error {
	if _, err := s.coll.InsertOne(ctx, toDocument(n)); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Only the path index can realistically collide; ids are fresh UUIDs.
			return note.ErrPathTaken
		}
		return fmt.Errorf("inserting note: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (note.Note, error) {
	var doc document
	err := s.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return note.Note{}, note.ErrNotFound
	}
	if err != nil {
		return note.Note{}, fmt.Errorf("finding note: %w", err)
	}
	return doc.toNote(), nil
}

func (s *Store) Update(ctx context.Context, n note.Note, expectedVersion int64) (note.Note, error) {
	return s.compareAndSet(ctx, n.ID, expectedVersion, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "path", Value: n.Path},
			{Key: "body", Value: n.Body},
			{Key: "tags", Value: toDocument(n).Tags},
			{Key: "updatedAt", Value: n.UpdatedAt},
			{Key: "updatedBy", Value: n.UpdatedBy},
		}},
		{Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}},
	})
}

func (s *Store) Delete(ctx context.Context, id string, expectedVersion int64, by string, at time.Time) (note.Note, error) {
	return s.compareAndSet(ctx, id, expectedVersion, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "deleted", Value: true},
			{Key: "updatedAt", Value: at},
			{Key: "updatedBy", Value: by},
		}},
		{Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}},
	})
}

// compareAndSet applies update only if the note is live and at expectedVersion,
// in one server-side operation, so concurrent writers cannot both win.
func (s *Store) compareAndSet(ctx context.Context, id string, expectedVersion int64, update bson.D) (note.Note, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "version", Value: expectedVersion},
		{Key: "deleted", Value: false},
	}
	var doc document
	err := s.coll.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
	switch {
	case err == nil:
		return doc.toNote(), nil
	case mongo.IsDuplicateKeyError(err):
		return note.Note{}, note.ErrPathTaken
	case errors.Is(err, mongo.ErrNoDocuments):
		// The filter matched nothing; a read tells missing/deleted from stale.
		current, getErr := s.Get(ctx, id)
		if getErr != nil {
			return note.Note{}, getErr
		}
		if current.Deleted {
			return note.Note{}, note.ErrNotFound
		}
		return note.Note{}, note.ErrVersionConflict
	default:
		return note.Note{}, fmt.Errorf("updating note: %w", err)
	}
}

func (s *Store) List(ctx context.Context, q note.ListQuery) ([]note.Note, error) {
	opts := options.Find().SetSort(bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}})
	if q.Limit > 0 {
		opts.SetLimit(int64(q.Limit))
	}
	cursor, err := s.coll.Find(ctx, listFilter(q), opts)
	if err != nil {
		return nil, fmt.Errorf("finding notes: %w", err)
	}
	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("reading notes: %w", err)
	}
	notes := make([]note.Note, len(docs))
	for i, doc := range docs {
		notes[i] = doc.toNote()
	}
	return notes, nil
}

func listFilter(q note.ListQuery) bson.D {
	filter := bson.D{}
	if !q.IncludeDeleted {
		filter = append(filter, bson.E{Key: "deleted", Value: false})
	}
	if q.Prefix != "" {
		// Anchored and quoted: the prefix is user input, not a pattern.
		filter = append(filter, bson.E{Key: "path", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(q.Prefix)}}})
	}
	if q.Tag != "" {
		filter = append(filter, bson.E{Key: "tags", Value: q.Tag})
	}
	if q.Text != "" {
		filter = append(filter, bson.E{Key: "$text", Value: bson.D{{Key: "$search", Value: q.Text}}})
	}
	if q.UpdatedSince != nil {
		filter = append(filter, bson.E{Key: "updatedAt", Value: bson.D{{Key: "$gte", Value: *q.UpdatedSince}}})
	}
	if q.After != nil {
		// Keyset condition on the sort key: strictly after (updatedAt, _id).
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "updatedAt", Value: bson.D{{Key: "$gt", Value: q.After.UpdatedAt}}}},
			bson.D{{Key: "updatedAt", Value: q.After.UpdatedAt}, {Key: "_id", Value: bson.D{{Key: "$gt", Value: q.After.ID}}}},
		}})
	}
	return filter
}

func (s *Store) Folders(ctx context.Context, prefix string) ([]note.Folder, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "deleted", Value: false},
			{Key: "path", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(prefix)}}},
		}}},
		// Split what follows the prefix; a note directly in the folder yields one
		// part and is dropped, a note in a subfolder yields the folder name first.
		{{Key: "$project", Value: bson.D{
			{Key: "parts", Value: bson.D{{Key: "$split", Value: bson.A{
				bson.D{{Key: "$substrBytes", Value: bson.A{"$path", len(prefix), -1}}},
				"/",
			}}}},
		}}},
		{{Key: "$match", Value: bson.D{{Key: "$expr", Value: bson.D{
			{Key: "$gt", Value: bson.A{bson.D{{Key: "$size", Value: "$parts"}}, 1}},
		}}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$arrayElemAt", Value: bson.A{"$parts", 0}}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cursor, err := s.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregating folders: %w", err)
	}
	var rows []struct {
		Name  string `bson:"_id"`
		Count int    `bson:"count"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("reading folders: %w", err)
	}
	folders := make([]note.Folder, len(rows))
	for i, row := range rows {
		folders[i] = note.Folder{Path: prefix + row.Name + "/", Name: row.Name, NoteCount: row.Count}
	}
	return folders, nil
}
