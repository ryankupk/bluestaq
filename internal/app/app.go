// Package app is the composition root: it wires the store, event hub, service
// and HTTP router together. Both cmd/notesd and the end-to-end tests build the
// service through New, so the tests exercise exactly the production wiring.
package app

import (
	"context"
	"log/slog"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/ryankupk/notes/internal/events"
	"github.com/ryankupk/notes/internal/httpapi"
	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/mongostore"
)

// App is a fully wired service.
type App struct {
	handler http.Handler
	hub     *events.Hub
}

// mongoPinger adapts the driver client to httpapi.Pinger.
type mongoPinger struct{ client *mongo.Client }

func (p mongoPinger) Ping(ctx context.Context) error {
	return p.client.Ping(ctx, readpref.Primary())
}

// New ensures the collection's indexes and wires the service on top of
// collection. The caller owns the Mongo client and its lifecycle.
func New(ctx context.Context, collection *mongo.Collection, logger *slog.Logger) (*App, error) {
	store := mongostore.New(collection)
	if err := store.EnsureIndexes(ctx); err != nil {
		return nil, err
	}

	hub := events.NewHub(events.DefaultBuffer)
	notes := note.NewService(store, hub, nil, nil)
	router := httpapi.NewRouter(httpapi.Deps{
		Pinger: mongoPinger{collection.Database().Client()},
		Notes:  notes,
		Hub:    hub,
		Logger: logger,
	})
	return &App{handler: router, hub: hub}, nil
}

// Handler is the HTTP entry point.
func (a *App) Handler() http.Handler { return a.handler }

// Close closes every live WebSocket with 1001 Going Away. It is idempotent and
// does not touch the Mongo client. Call it before http.Server.Shutdown, which
// does not track hijacked connections.
func (a *App) Close() {
	a.hub.Close()
}
