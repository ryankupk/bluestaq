// Package config loads service settings from the environment.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds every setting the service reads at startup.
type Config struct {
	HTTPAddr        string
	MongoURI        string
	MongoDB         string
	NotesCollection string
	LogLevel        slog.Level
}

// Load reads an optional .env file and then the process environment.
// godotenv.Load never overrides variables that are already set, so real
// environment variables always win over .env, which is what containers and CI expect.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("reading .env: %w", err)
	}

	level, err := parseLevel(getenv("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		HTTPAddr:        getenv("HTTP_ADDR", ":8080"),
		MongoURI:        getenv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:         getenv("MONGO_DB", "notes"),
		NotesCollection: getenv("MONGO_NOTES_COLLECTION", "notes"),
		LogLevel:        level,
	}, nil
}

// getenv treats an empty value like an unset one so that `FOO=` in a .env
// file falls back to the default instead of producing a blank setting.
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(raw))); err != nil {
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: %w", raw, err)
	}
	return level, nil
}
