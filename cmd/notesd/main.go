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

	"github.com/ryankupk/notes/internal/app"
	"github.com/ryankupk/notes/internal/config"
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

	indexCtx, cancelIndexes := context.WithTimeout(ctx, indexTimeout)
	defer cancelIndexes()
	service, err := app.New(indexCtx, client.Database(cfg.MongoDB).Collection(cfg.NotesCollection), logger)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           service.Handler(),
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
	// Hijacked WebSocket connections are invisible to server.Shutdown, so close
	// them (with 1001 Going Away) through the hub first.
	service.Close()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
