// Package config loads service settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/ryankupk/notes/internal/auth"
)

// AuthMode selects how callers get a token.
type AuthMode string

const (
	// AuthDev exposes POST /api/v1/auth/dev-token so anyone can mint a token by
	// naming themselves. It is for demos only: it proves nothing about identity.
	AuthDev AuthMode = "dev"
	// AuthJWT accepts only tokens signed elsewhere with the shared JWT_SECRET.
	AuthJWT AuthMode = "jwt"
)

const defaultJWTTTL = 12 * time.Hour

// Config holds every setting the service reads at startup.
type Config struct {
	HTTPAddr        string
	MongoURI        string
	MongoDB         string
	NotesCollection string
	LogLevel        slog.Level

	AuthMode AuthMode
	// JWTSecret is the HS256 signing key (at least auth.MinSecretBytes).
	JWTSecret string
	// JWTSecretGenerated reports that dev mode made up JWTSecret because none
	// was set, so the caller can warn that tokens die with the process.
	JWTSecretGenerated bool
	JWTTTL             time.Duration
	// CORSAllowedOrigins empty means no CORS headers at all (same-origin only).
	CORSAllowedOrigins []string
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

	cfg := Config{
		HTTPAddr:           getenv("HTTP_ADDR", ":8080"),
		MongoURI:           getenv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:            getenv("MONGO_DB", "notes"),
		NotesCollection:    getenv("MONGO_NOTES_COLLECTION", "notes"),
		LogLevel:           level,
		CORSAllowedOrigins: parseOrigins(os.Getenv("CORS_ALLOWED_ORIGINS")),
	}
	if err := loadAuth(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadAuth fills the authentication settings. AUTH_MODE has no default on
// purpose: silently falling back to dev would turn a forgotten variable in
// production into "anyone can mint a token".
func loadAuth(cfg *Config) error {
	switch mode := AuthMode(strings.TrimSpace(os.Getenv("AUTH_MODE"))); mode {
	case AuthDev, AuthJWT:
		cfg.AuthMode = mode
	case "":
		return errors.New("AUTH_MODE must be set to dev or jwt; use dev for local demos")
	default:
		return fmt.Errorf("AUTH_MODE must be set to dev or jwt; use dev for local demos (got %q)", mode)
	}

	ttl, err := time.ParseDuration(getenv("JWT_TTL", defaultJWTTTL.String()))
	if err != nil || ttl <= 0 {
		return fmt.Errorf("invalid JWT_TTL %q: want a positive duration such as 12h", os.Getenv("JWT_TTL"))
	}
	cfg.JWTTTL = ttl

	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	switch {
	case cfg.JWTSecret == "" && cfg.AuthMode == AuthDev:
		raw := make([]byte, auth.MinSecretBytes)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generating dev JWT secret: %w", err)
		}
		cfg.JWTSecret = hex.EncodeToString(raw)
		cfg.JWTSecretGenerated = true
	case cfg.JWTSecret == "":
		return errors.New("JWT_SECRET is required when AUTH_MODE=jwt")
	case len(cfg.JWTSecret) < auth.MinSecretBytes:
		return fmt.Errorf("JWT_SECRET must be at least %d bytes, got %d", auth.MinSecretBytes, len(cfg.JWTSecret))
	}
	return nil
}

// parseOrigins splits a comma-separated list, dropping blanks and trailing
// slashes so "http://localhost:5173/" still matches the browser's Origin header.
func parseOrigins(raw string) []string {
	var origins []string
	for _, origin := range strings.Split(raw, ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
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
