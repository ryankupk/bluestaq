package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// chdirTemp moves into an empty directory so a developer's real .env cannot leak into tests.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_ADDR", "MONGO_URI", "MONGO_DB", "MONGO_NOTES_COLLECTION", "LOG_LEVEL"} {
		// t.Setenv registers restoration of the original value; the unset makes
		// the variable truly absent, since godotenv treats even an empty value as "set".
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		dotenv  string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			want: Config{":8080", "mongodb://localhost:27017", "notes", "notes", slog.LevelInfo},
		},
		{
			name: "env overrides defaults",
			env: map[string]string{
				"HTTP_ADDR": ":9000", "MONGO_URI": "mongodb://mongo:27017", "MONGO_DB": "teamb",
				"MONGO_NOTES_COLLECTION": "docs", "LOG_LEVEL": "debug",
			},
			want: Config{":9000", "mongodb://mongo:27017", "teamb", "docs", slog.LevelDebug},
		},
		{
			name:   "dotenv fills gaps",
			dotenv: "MONGO_DB=fromfile\nLOG_LEVEL=warn\n",
			want:   Config{":8080", "mongodb://localhost:27017", "fromfile", "notes", slog.LevelWarn},
		},
		{
			name:   "real env beats dotenv",
			env:    map[string]string{"MONGO_DB": "fromenv"},
			dotenv: "MONGO_DB=fromfile\n",
			want:   Config{":8080", "mongodb://localhost:27017", "fromenv", "notes", slog.LevelInfo},
		},
		{
			name:    "bad log level",
			env:     map[string]string{"LOG_LEVEL": "loud"},
			wantErr: true,
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
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
