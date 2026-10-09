package mongostore_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/mongostore"
	"github.com/ryankupk/notes/internal/store/storetest"
)

// newRepo gives each subtest its own throwaway database so cases cannot see
// each other's data and can run against any shared MongoDB.
func newRepo(t *testing.T) note.Repository {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" || testing.Short() {
		t.Skip("MONGO_URI unset or -short; skipping MongoDB tests")
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	db := client.Database(fmt.Sprintf("notes_test_%d_%s", time.Now().UnixNano(), hex.EncodeToString(suffix)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Errorf("dropping test database: %v", err)
		}
		if err := client.Disconnect(ctx); err != nil {
			t.Errorf("disconnect: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := mongostore.New(db.Collection("notes"))
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	return store
}

func TestConformance(t *testing.T) {
	storetest.Run(t, newRepo)
}

func TestEnsureIndexesIsIdempotent(t *testing.T) {
	repo := newRepo(t).(*mongostore.Store)
	if err := repo.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
}
