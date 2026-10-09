// Command notes-watch connects to the notes WebSocket and prints every frame
// it receives as one JSON line. It stands in for websocat during review.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
		user    = flag.String("user", "", "dev mode: fetch a token for this user name from the server")
		token   = flag.String("token", "", "use this JWT as-is (for servers in jwt mode)")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bearer := *token
	switch {
	case bearer != "" && *user != "":
		return errors.New("use either -user or -token, not both")
	case bearer == "" && *user == "":
		return errors.New("one of -user (dev mode) or -token is required")
	case *user != "":
		var err error
		if bearer, err = fetchDevToken(ctx, u, *user); err != nil {
			return err
		}
	}
	// Browsers cannot set headers on a WebSocket handshake, so the server takes
	// the token as a query parameter; we use the same path to stay representative.
	q.Set("access_token", bearer)
	u.RawQuery = q.Encode()

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, u.String(), nil)
	cancel()
	if err != nil {
		// Redact: the URL carries the token and error output lands in terminals and CI logs.
		return fmt.Errorf("connecting to %s: %w", strings.ReplaceAll(u.String(), bearer, "REDACTED"), err)
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

// fetchDevToken asks a dev-mode server for a token. The HTTP base is derived
// from the WebSocket URL (ws -> http, wss -> https).
func fetchDevToken(ctx context.Context, wsURL *url.URL, user string) (string, error) {
	base := *wsURL
	switch base.Scheme {
	case "ws":
		base.Scheme = "http"
	case "wss":
		base.Scheme = "https"
	}
	base.Path, base.RawQuery = "/api/v1/auth/dev-token", ""

	payload, err := json.Marshal(map[string]string{"user": user})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching dev token from %s: %w", base.String(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching dev token: %s (is the server in AUTH_MODE=dev? otherwise pass -token)", resp.Status)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Token == "" {
		return "", fmt.Errorf("decoding dev token response: %v", err)
	}
	return body.Token, nil
}
