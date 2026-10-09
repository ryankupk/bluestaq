package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGzip(t *testing.T) {
	e := newEnv(t)
	big := e.create("eng/big.md", strings.Repeat("# heading\nsome markdown text\n", 200))
	small := e.create("eng/small.md", "hi")

	t.Run("large response is compressed and round-trips", func(t *testing.T) {
		rec := e.do(http.MethodGet, notesURL+"/"+big.ID, "", "Accept-Encoding", "gzip")
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plain), "some markdown text") {
			t.Fatalf("decompressed body missing note text: %.80s", plain)
		}
		// The ETag must survive compression so If-Match and If-None-Match keep working.
		if rec.Header().Get("ETag") != `"1"` {
			t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
		}
	})

	t.Run("small response is not compressed", func(t *testing.T) {
		rec := e.do(http.MethodGet, notesURL+"/"+small.ID, "", "Accept-Encoding", "gzip")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want none", got)
		}
	})

	t.Run("client without gzip gets plain JSON", func(t *testing.T) {
		rec := e.do(http.MethodGet, notesURL+"/"+big.ID, "")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want none", got)
		}
	})
}
