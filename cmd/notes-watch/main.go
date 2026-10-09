// Command notes-watch connects to the notes WebSocket and prints every frame
// it receives as one JSON line. It stands in for websocat during review.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notes-watch:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		rawURL  = flag.String("url", "ws://localhost:8080/api/v1/ws", "WebSocket endpoint")
		prefix  = flag.String("prefix", "", "only receive events under this path prefix")
		user    = flag.String("user", "", "identity to present (default: anonymous)")
		editing = flag.String("editing", "", "note ID to announce as being edited after connecting")
	)
	flag.Parse()

	u, err := url.Parse(*rawURL)
	if err != nil {
		return fmt.Errorf("parsing -url: %w", err)
	}
	q := u.Query()
	if *prefix != "" {
		q.Set("prefix", *prefix)
	}
	if *user != "" {
		q.Set("user", *user)
	}
	u.RawQuery = q.Encode()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, u.String(), nil)
	cancel()
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", u, err)
	}
	defer conn.CloseNow()

	if *editing != "" {
		msg := map[string]string{"type": "editing", "noteId": *editing}
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			return fmt.Errorf("sending editing message: %w", err)
		}
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Ctrl-C: say goodbye properly instead of dropping the TCP connection.
				_ = conn.Close(websocket.StatusNormalClosure, "bye")
				return nil
			}
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return fmt.Errorf("reading: %w", err)
		}
		if typ == websocket.MessageText {
			fmt.Printf("%s\n", bytes.TrimSpace(data)) // wsjson frames end in a newline
		}
	}
}
