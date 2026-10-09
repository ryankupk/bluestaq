// Command loadtest drives the notes service with many simulated editors, each
// optionally holding a WebSocket on its team folder, and prints a summary of
// throughput, conflicts, latency and event delivery lag. It creates its notes
// under loadtest/<run>/ and deletes them again before exiting.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type config struct {
	base          string
	users         int
	teamSize      int
	sharedPerTeam int
	sharedRatio   float64
	duration      time.Duration
	noteBytes     int
	think         time.Duration
	useWS         bool
}

// handle is one user's view of one note: the version it last saw is
// deliberately never refreshed from WebSocket events.
type handle struct {
	id, path string
	version  int64
	body     []byte
}

// userStats is owned by one user (editor goroutine plus WS reader goroutine
// touch disjoint fields), merged after the run so no locks are needed.
type userStats struct {
	latencies []time.Duration
	attempts  int
	edits     int
	conflicts int
	errors    map[string]int
	frames    map[string]int
	lags      []time.Duration
	closes    map[string]int
}

type user struct {
	name, token, teamPrefix string
	team                    int
	handles                 []*handle // shared handles first, private last
	stats                   userStats
}

type loader struct {
	cfg       config
	client    *http.Client
	runID     string
	measuring atomic.Bool
	users     []*user
	created   []createdNote
}

type createdNote struct{ id, token string }

func main() {
	var cfg config
	flag.StringVar(&cfg.base, "base", "http://localhost:8080", "service base URL")
	flag.IntVar(&cfg.users, "users", 100, "simulated users")
	flag.IntVar(&cfg.teamSize, "team-size", 10, "users per team folder")
	flag.IntVar(&cfg.sharedPerTeam, "shared-per-team", 3, "shared notes per team")
	flag.Float64Var(&cfg.sharedRatio, "shared-ratio", 0.3, "probability an edit targets a shared team note")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "how long to edit")
	flag.IntVar(&cfg.noteBytes, "note-bytes", 4096, "body size of every note")
	flag.DurationVar(&cfg.think, "think", 0, "pause between a user's edits")
	flag.BoolVar(&cfg.useWS, "ws", true, "each user holds a WebSocket on its team folder")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newLoader(cfg).run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func newLoader(cfg config) *loader {
	transport := &http.Transport{
		MaxIdleConns:        cfg.users + 64,
		MaxIdleConnsPerHost: cfg.users + 64,
		IdleConnTimeout:     90 * time.Second,
	}
	return &loader{
		cfg:    cfg,
		client: &http.Client{Transport: transport, Timeout: 30 * time.Second},
		runID:  strconv.FormatInt(time.Now().Unix(), 36) + strconv.Itoa(rand.IntN(100)),
	}
}

func (l *loader) run(ctx context.Context) (err error) {
	var sockets []*websocket.Conn
	var readers sync.WaitGroup
	readCtx, stopReaders := context.WithCancel(context.Background())
	defer func() {
		// Cleanup runs even after Ctrl-C, so it must not use ctx.
		l.cleanup()
		stopReaders()
		for _, conn := range sockets {
			conn.CloseNow()
		}
		readers.Wait()
	}()

	if err := l.setup(ctx); err != nil {
		return err
	}
	if l.cfg.useWS {
		if sockets, err = l.openSockets(ctx, readCtx, &readers); err != nil {
			return err
		}
	}

	fmt.Printf("run %s: %d users, %d teams, editing for %s\n", l.runID, l.cfg.users, (l.cfg.users+l.cfg.teamSize-1)/l.cfg.teamSize, l.cfg.duration)
	start := time.Now()
	deadline := start.Add(l.cfg.duration)
	l.measuring.Store(true)
	var editors sync.WaitGroup
	for _, u := range l.users {
		editors.Add(1)
		go func() {
			defer editors.Done()
			l.edit(ctx, u, deadline)
		}()
	}
	editors.Wait()
	l.measuring.Store(false)
	l.report(time.Since(start))
	return nil
}

func (l *loader) setup(ctx context.Context) error {
	prefix := "loadtest/" + l.runID
	body := func() []byte { return randomText(l.cfg.noteBytes) }
	l.users = make([]*user, l.cfg.users)
	for i := range l.users {
		team := i / l.cfg.teamSize
		l.users[i] = &user{
			name: fmt.Sprintf("lt-%s-%d", l.runID, i), team: team,
			teamPrefix: fmt.Sprintf("%s/team-%d/", prefix, team),
			stats:      userStats{errors: map[string]int{}, frames: map[string]int{}, closes: map[string]int{}},
		}
	}
	if err := parallel(l.cfg.users, 32, func(i int) error {
		var resp struct{ Token string }
		_, err := l.do(ctx, http.MethodPost, "/api/v1/auth/dev-token", "", nil, map[string]string{"user": l.users[i].name}, &resp)
		l.users[i].token = resp.Token
		return err
	}); err != nil {
		return fmt.Errorf("minting tokens: %w", err)
	}

	// The first user of each team creates its shared notes; teammates copy the handles.
	teams := (l.cfg.users + l.cfg.teamSize - 1) / l.cfg.teamSize
	shared := make([][]*handle, teams)
	var mu sync.Mutex
	create := func(owner *user, path string, text []byte) (*handle, error) {
		var resp struct {
			ID      string
			Version int64
		}
		_, err := l.do(ctx, http.MethodPost, "/api/v1/notes", owner.token, nil,
			map[string]any{"path": path, "body": string(text), "tags": []string{}}, &resp)
		if err != nil {
			return nil, fmt.Errorf("creating %s: %w", path, err)
		}
		mu.Lock()
		l.created = append(l.created, createdNote{resp.ID, owner.token})
		mu.Unlock()
		return &handle{id: resp.ID, path: path, version: resp.Version, body: text}, nil
	}
	if err := parallel(teams, 16, func(team int) error {
		owner := l.users[team*l.cfg.teamSize]
		for n := range l.cfg.sharedPerTeam {
			h, err := create(owner, fmt.Sprintf("%sshared-%d.md", owner.teamPrefix, n), body())
			if err != nil {
				return err
			}
			shared[team] = append(shared[team], h)
		}
		return nil
	}); err != nil {
		return err
	}
	return parallel(l.cfg.users, 32, func(i int) error {
		u := l.users[i]
		for _, h := range shared[u.team] {
			u.handles = append(u.handles, &handle{h.id, h.path, h.version, append([]byte(nil), h.body...)})
		}
		private, err := create(u, fmt.Sprintf("%suser-%d.md", u.teamPrefix, i), body())
		u.handles = append(u.handles, private)
		return err
	})
}

func (l *loader) openSockets(ctx, readCtx context.Context, readers *sync.WaitGroup) ([]*websocket.Conn, error) {
	wsBase := strings.Replace(l.cfg.base, "http", "ws", 1) + "/api/v1/ws"
	conns := make([]*websocket.Conn, len(l.users))
	err := parallel(len(l.users), 64, func(i int) error {
		u := l.users[i]
		query := url.Values{"prefix": {u.teamPrefix}, "access_token": {u.token}}
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(dialCtx, wsBase+"?"+query.Encode(), nil)
		if err != nil {
			return fmt.Errorf("dialing websocket for %s: %w", u.name, err)
		}
		conn.SetReadLimit(1 << 20)
		conns[i] = conn
		_, data, err := conn.Read(dialCtx)
		if err != nil || !bytes.Contains(data, []byte(`"hello"`)) {
			return fmt.Errorf("waiting for hello for %s: %v (%s)", u.name, err, data)
		}
		return nil
	})
	if err != nil {
		for _, conn := range conns {
			if conn != nil {
				conn.CloseNow()
			}
		}
		return nil, err
	}
	for i, conn := range conns {
		readers.Add(1)
		go func() {
			defer readers.Done()
			l.readEvents(readCtx, conn, l.users[i])
		}()
	}
	return conns, nil
}

func (l *loader) readEvents(ctx context.Context, conn *websocket.Conn, u *user) {
	for {
		_, data, err := conn.Read(ctx)
		received := time.Now()
		if err != nil {
			if ctx.Err() == nil {
				code := "no-close-frame"
				if status := websocket.CloseStatus(err); status != -1 {
					code = strconv.Itoa(int(status))
				}
				u.stats.closes[code]++ // only this goroutine writes closes
			}
			return
		}
		if !l.measuring.Load() {
			continue
		}
		var ev struct {
			Type string
			Note *struct{ UpdatedAt time.Time }
		}
		if json.Unmarshal(data, &ev) != nil {
			u.stats.frames["undecodable"]++
			continue
		}
		u.stats.frames[ev.Type]++
		if strings.HasPrefix(ev.Type, "note.") && ev.Note != nil {
			u.stats.lags = append(u.stats.lags, max(0, received.Sub(ev.Note.UpdatedAt)))
		}
	}
}

func (l *loader) edit(ctx context.Context, u *user, deadline time.Time) {
	st := &u.stats
	if l.cfg.think > 0 { // spread the start so autosave-style users don't move in lockstep
		if !sleep(ctx, rand.N(l.cfg.think)) {
			return
		}
	}
	for time.Now().Before(deadline) && ctx.Err() == nil {
		h := u.handles[len(u.handles)-1]
		if l.cfg.sharedPerTeam > 0 && rand.Float64() < l.cfg.sharedRatio {
			h = u.handles[rand.IntN(len(u.handles)-1)]
		}
		mutate(h.body)
		for ctx.Err() == nil {
			begin := time.Now()
			var resp struct {
				Current struct{ Version int64 }
			}
			status, err := l.do(ctx, http.MethodPut, "/api/v1/notes/"+h.id, u.token,
				http.Header{"If-Match": {strconv.Quote(strconv.FormatInt(h.version, 10))}},
				map[string]any{"path": h.path, "body": string(h.body), "tags": []string{}}, &resp)
			if ctx.Err() != nil {
				break
			}
			st.attempts++
			st.latencies = append(st.latencies, time.Since(begin))
			if status == http.StatusPreconditionFailed {
				st.conflicts++
				h.version = resp.Current.Version
				continue // retry the same edit on the fresh version
			}
			if err != nil || status != http.StatusOK {
				st.errors[fmt.Sprintf("status %d", status)]++
				break
			}
			st.edits++
			h.version++ // our write moved it from the version we sent
			break
		}
		if l.cfg.think > 0 && !sleep(ctx, l.cfg.think) {
			return
		}
	}
}

// do sends one JSON request. It returns the status even when the status was an
// error, and decodes JSON into out for 2xx and 412 responses.
func (l *loader) do(ctx context.Context, method, path, token string, header http.Header, payload, out any) (int, error) {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.cfg.base+path, reader)
	if err != nil {
		return 0, err
	}
	for key, values := range header {
		req.Header[key] = values
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && (resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusPreconditionFailed) {
		err = json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
	if err == nil && resp.StatusCode >= 300 && resp.StatusCode != http.StatusPreconditionFailed {
		err = fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return resp.StatusCode, err
}

// cleanup soft-deletes every created note. It deliberately ignores the run's
// cancelled context.
func (l *loader) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var failed atomic.Int64
	_ = parallel(len(l.created), 32, func(i int) error {
		n := l.created[i]
		for range 50 {
			var current struct{ Version int64 }
			status, err := l.do(ctx, http.MethodGet, "/api/v1/notes/"+n.id, n.token, nil, nil, &current)
			if status == http.StatusNotFound {
				return nil
			}
			if err != nil {
				break
			}
			status, _ = l.do(ctx, http.MethodDelete, "/api/v1/notes/"+n.id, n.token,
				http.Header{"If-Match": {strconv.Quote(strconv.FormatInt(current.Version, 10))}}, nil, nil)
			if status == http.StatusNoContent || status == http.StatusNotFound {
				return nil
			}
			if status != http.StatusPreconditionFailed {
				break
			}
		}
		failed.Add(1)
		return nil
	})
	fmt.Printf("cleanup: deleted %d notes, %d failed\n", len(l.created)-int(failed.Load()), failed.Load())
}

func (l *loader) report(elapsed time.Duration) {
	var total userStats
	total.errors, total.frames, total.closes = map[string]int{}, map[string]int{}, map[string]int{}
	for _, u := range l.users {
		s := &u.stats
		total.attempts += s.attempts
		total.edits += s.edits
		total.conflicts += s.conflicts
		total.latencies = append(total.latencies, s.latencies...)
		total.lags = append(total.lags, s.lags...)
		for _, m := range [][2]map[string]int{{total.errors, s.errors}, {total.frames, s.frames}, {total.closes, s.closes}} {
			for k, v := range m[1] {
				m[0][k] += v
			}
		}
	}
	secs := elapsed.Seconds()
	errorCount, frameCount, noteFrames := 0, 0, 0
	for _, v := range total.errors {
		errorCount += v
	}
	for k, v := range total.frames {
		frameCount += v
		if strings.HasPrefix(k, "note.") {
			noteFrames += v
		}
	}
	fmt.Printf("\nusers=%d think=%s elapsed=%.1fs ws=%v\n", l.cfg.users, l.cfg.think, secs, l.cfg.useWS)
	fmt.Printf("successful edits: %d (%.1f/s)\n", total.edits, float64(total.edits)/secs)
	fmt.Printf("PUT attempts: %d, conflicts (412): %d (%.1f%% of attempts), errors: %d %v\n",
		total.attempts, total.conflicts, pct(total.conflicts, total.attempts), errorCount, total.errors)
	fmt.Printf("PUT latency: %s\n", summarize(total.latencies))
	if l.cfg.useWS {
		fmt.Printf("WS frames: %d (%.1f/s) by type %v\n", frameCount, float64(frameCount)/secs, total.frames)
		fmt.Printf("event lag: %s\n", summarize(total.lags))
		fmt.Printf("WS early closes: %v\n", total.closes)
		expected := total.edits * l.cfg.teamSize
		fmt.Printf("fan-out: expected ~%d note events (edits x team-size), observed %d (%.1f%%)\n", expected, noteFrames, pct(noteFrames, expected))
	}
}

func summarize(values []time.Duration) string {
	if len(values) == 0 {
		return "no samples"
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	at := func(q float64) time.Duration { return values[min(len(values)-1, int(q*float64(len(values))))] }
	return fmt.Sprintf("n=%d p50=%s p95=%s p99=%s max=%s", len(values),
		at(0.50).Round(time.Microsecond), at(0.95).Round(time.Microsecond), at(0.99).Round(time.Microsecond), values[len(values)-1].Round(time.Microsecond))
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

// parallel runs fn(0..n-1) with at most limit in flight and returns the first error.
func parallel(n, limit int, fn func(i int) error) error {
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
		slots    = make(chan struct{}, limit)
	)
	for i := range n {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := fn(i); err != nil {
				once.Do(func() { firstErr = err })
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

const alphabet = "abcdefghijklmnopqrstuvwxyz  \n"

func randomText(n int) []byte {
	text := make([]byte, n)
	for i := range text {
		text[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return text
}

// mutate replaces a few bytes at a random offset.
func mutate(body []byte) {
	if len(body) == 0 {
		return
	}
	at := rand.IntN(len(body))
	for i := at; i < min(len(body), at+8); i++ {
		body[i] = alphabet[rand.IntN(len(alphabet))]
	}
}
