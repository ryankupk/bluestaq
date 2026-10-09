package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const goodSecret = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

// chdirTemp moves into an empty directory so a developer's real .env cannot leak into tests.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_ADDR", "MONGO_URI", "MONGO_DB", "MONGO_NOTES_COLLECTION", "LOG_LEVEL",
		"AUTH_MODE", "JWT_SECRET", "JWT_TTL", "CORS_ALLOWED_ORIGINS"} {
		// t.Setenv registers restoration of the original value; the unset makes
		// the variable truly absent, since godotenv treats even an empty value as "set".
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoad(t *testing.T) {
	devEnv := map[string]string{"AUTH_MODE": "dev", "JWT_SECRET": goodSecret}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range devEnv {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	base := Config{
		HTTPAddr: ":8080", MongoURI: "mongodb://localhost:27017", MongoDB: "notes", NotesCollection: "notes",
		LogLevel: slog.LevelInfo, AuthMode: AuthDev, JWTSecret: goodSecret, JWTTTL: 12 * time.Hour,
	}
	override := func(f func(*Config)) Config {
		c := base
		f(&c)
		return c
	}

	tests := []struct {
		name    string
		env     map[string]string
		dotenv  string
		want    Config
		wantErr string // substring of the error; empty means success
	}{
		{name: "defaults", env: devEnv, want: base},
		{
			name: "env overrides defaults",
			env: with(map[string]string{
				"HTTP_ADDR": ":9000", "MONGO_URI": "mongodb://mongo:27017", "MONGO_DB": "teamb",
				"MONGO_NOTES_COLLECTION": "docs", "LOG_LEVEL": "debug",
			}),
			want: override(func(c *Config) {
				c.HTTPAddr, c.MongoURI, c.MongoDB, c.NotesCollection, c.LogLevel = ":9000", "mongodb://mongo:27017", "teamb", "docs", slog.LevelDebug
			}),
		},
		{
			name:   "dotenv fills gaps",
			env:    devEnv,
			dotenv: "MONGO_DB=fromfile\nLOG_LEVEL=warn\n",
			want:   override(func(c *Config) { c.MongoDB, c.LogLevel = "fromfile", slog.LevelWarn }),
		},
		{
			name:   "real env beats dotenv",
			env:    with(map[string]string{"MONGO_DB": "fromenv"}),
			dotenv: "MONGO_DB=fromfile\n",
			want:   override(func(c *Config) { c.MongoDB = "fromenv" }),
		},
		{name: "bad log level", env: with(map[string]string{"LOG_LEVEL": "loud"}), wantErr: "LOG_LEVEL"},

		{
			name: "jwt mode with secret",
			env:  map[string]string{"AUTH_MODE": "jwt", "JWT_SECRET": goodSecret},
			want: override(func(c *Config) { c.AuthMode = AuthJWT }),
		},
		{name: "AUTH_MODE missing", wantErr: "AUTH_MODE must be set to dev or jwt; use dev for local demos"},
		{name: "AUTH_MODE invalid", env: map[string]string{"AUTH_MODE": "none"}, wantErr: "AUTH_MODE must be set to dev or jwt; use dev for local demos"},
		{name: "jwt mode needs secret", env: map[string]string{"AUTH_MODE": "jwt"}, wantErr: "JWT_SECRET is required"},
		{name: "jwt mode short secret", env: map[string]string{"AUTH_MODE": "jwt", "JWT_SECRET": "too-short"}, wantErr: "at least 32 bytes"},
		{name: "dev mode short secret", env: map[string]string{"AUTH_MODE": "dev", "JWT_SECRET": "too-short"}, wantErr: "at least 32 bytes"},
		{
			name: "dev mode generates secret",
			env:  map[string]string{"AUTH_MODE": "dev"},
			want: override(func(c *Config) { c.JWTSecretGenerated = true }),
		},
		{
			name: "custom ttl",
			env:  with(map[string]string{"JWT_TTL": "90m"}),
			want: override(func(c *Config) { c.JWTTTL = 90 * time.Minute }),
		},
		{name: "bad ttl", env: with(map[string]string{"JWT_TTL": "tomorrow"}), wantErr: "JWT_TTL"},
		{name: "non-positive ttl", env: with(map[string]string{"JWT_TTL": "-1h"}), wantErr: "JWT_TTL"},
		{
			name: "cors origins are split and trimmed",
			env:  with(map[string]string{"CORS_ALLOWED_ORIGINS": " http://localhost:5173/, https://notes.example.com ,,"}),
			want: override(func(c *Config) {
				c.CORSAllowedOrigins = []string{"http://localhost:5173", "https://notes.example.com"}
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := chdirTemp(t)
			clearEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if tt.dotenv != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tt.dotenv), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if tt.want.JWTSecretGenerated {
				if len(got.JWTSecret) < 32 {
					t.Errorf("generated secret too short: %d bytes", len(got.JWTSecret))
				}
				got.JWTSecret = goodSecret // random, so normalize before comparing
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGeneratedSecretsDiffer(t *testing.T) {
	chdirTemp(t)
	clearEnv(t)
	t.Setenv("AUTH_MODE", "dev")
	a, errA := Load()
	b, errB := Load()
	if errA != nil || errB != nil || a.JWTSecret == b.JWTSecret {
		t.Fatalf("secrets should be random per start: %v %v", errA, errB)
	}
}
