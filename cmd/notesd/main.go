// Command notesd runs the shared notes REST service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/ryankupk/notes/internal/config"
	"github.com/ryankupk/notes/internal/httpapi"
	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/mongostore"
)

const (
	connectTimeout  = 10 * time.Second
	indexTimeout    = 30 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notesd:", err)
		os.Exit(1)
	}
}

// mongoPinger adapts the driver client to httpapi.Pinger.
type mongoPinger struct{ client *mongo.Client }

func (p mongoPinger) Ping(ctx context.Context) error {
	return p.client.Ping(ctx, readpref.Primary())
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return fmt.Errorf("creating mongo client: %w", err)
	}
	defer func() {
		// Fresh context: ctx is already cancelled by the time we get here on shutdown.
		disconnectCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := client.Disconnect(disconnectCtx); err != nil {
			logger.Error("disconnecting mongo", "error", err)
		}
	}()

	// Connect is lazy in driver v2, so ping to fail fast on a bad URI or unreachable server.
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		return fmt.Errorf("connecting to mongo at %s: %w", cfg.MongoURI, err)
	}
	logger.Info("connected to mongo", "db", cfg.MongoDB)

	store := mongostore.New(client.Database(cfg.MongoDB).Collection(cfg.NotesCollection))
	indexCtx, cancelIndexes := context.WithTimeout(ctx, indexTimeout)
	defer cancelIndexes()
	if err := store.EnsureIndexes(indexCtx); err != nil {
		return err
	}

	// NopPublisher until the live-update hub exists.
	notes := note.NewService(store, note.NopPublisher{}, nil, nil)

	router := httpapi.NewRouter(httpapi.Deps{Pinger: mongoPinger{client}, Notes: notes, Logger: logger})
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
