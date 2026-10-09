package memstore_test

import (
	"testing"

	"github.com/ryankupk/notes/internal/note"
	"github.com/ryankupk/notes/internal/store/memstore"
	"github.com/ryankupk/notes/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) note.Repository { return memstore.New() })
}
