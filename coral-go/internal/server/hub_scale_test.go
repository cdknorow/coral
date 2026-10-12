package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// TestHubScale measures a hub with K fake remotes x N agents each and M
// dashboard websocket clients, at production polling intervals. It is opt-in:
//
//	CORAL_HUBSCALE=1 go test ./internal/server/ -run TestHubScale -v -count=1 -timeout 30m
//
// Knobs (env): CORAL_HUBSCALE_K (comma list, default "5,10,20"),
// CORAL_HUBSCALE_N (agents per remote, default 20), CORAL_HUBSCALE_M
// (dashboard clients, default 5), CORAL_HUBSCALE_WINDOW_S (measurement window
// per K, default 60, so per-minute numbers are direct), CORAL_HUBSCALE_FAULT_S
// (slow/offline phase, default 45; 0 skips it), CORAL_HUBSCALE_OUT (directory
// for CPU profiles; default a temp dir), CORAL_HUBSCALE_IDLE=1 (idle remotes:
// the feed sends only a coral_heartbeat every 15s and no diffs, like a real
// remote with nothing changing).
//
// Each fake remote serves GET /api/health, GET /api/sessions/live (a plain
// array of N ~1 KB sessions) and /ws/coral (a coral_update, then a coral_diff
// changing 2 sessions every 5s, each carrying a timestamped marker so the
// remote -> hub -> dashboard latency can be measured). CPU is profiled with
// pprof labels: role=hub for the hub's server and remote workers, role=remote
// and role=client for the fakes, so `go tool pprof -tagfocus=role=hub` isolates
// the hub. Everything runs in t.TempDir(); nothing touches ~/.coral.
func TestHubScale(t *testing.T) {
	if os.Getenv("CORAL_HUBSCALE") != "1" {
		t.Skip("set CORAL_HUBSCALE=1 to run the hub scale test")
	}
	ks := []int{5, 10, 20}
	if v := os.Getenv("CORAL_HUBSCALE_K"); v != "" {
		ks = nil
		for _, f := range strings.Split(v, ",") {
			k, err := strconv.Atoi(strings.TrimSpace(f))
			require.NoError(t, err)
			ks = append(ks, k)
		}
	}
	n := hubScaleEnvInt("CORAL_HUBSCALE_N", 20)
	m := hubScaleEnvInt("CORAL_HUBSCALE_M", 5)
	window := time.Duration(hubScaleEnvInt("CORAL_HUBSCALE_WINDOW_S", 60)) * time.Second
	faultFor := time.Duration(hubScaleEnvInt("CORAL_HUBSCALE_FAULT_S", 45)) * time.Second
	outDir := os.Getenv("CORAL_HUBSCALE_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	// The e2e env quiets logs and isolates TMPDIR etc.; then restore
	// production timings (it shortens them for functional tests).
	newE2EEnv(t)
	remoteAgentHubConfig = background.RemoteAgentHubConfig{} // zero = production defaults (5s poll, 10s timeout, 1-30s backoff)

	for i, k := range ks {
		fault := faultFor > 0 && i == len(ks)/2 // the fault phase runs once, on the middle K
		t.Run(fmt.Sprintf("K=%d_N=%d_M=%d", k, n, m), func(t *testing.T) {
			runHubScale(t, k, n, m, window, fault, faultFor, outDir)
		})
	}
}

func runHubScale(t *testing.T, k, n, m int, window time.Duration, fault bool, faultFor time.Duration, outDir string) {
	baseGoroutines := runtime.NumGoroutine()
	hub := newScaleHub(t)
	defer hub.close()

	remotes := make([]*fakeRemote, k)
	for i := range remotes {
		remotes[i] = newFakeRemote(t, fmt.Sprintf("r%02d", i), n)
		reg := hub.register(remotes[i].id, remotes[i].url(), "scale-key", true)
		require.Equal(t, http.StatusCreated, reg.Code, "%v", reg.Body)
	}
	// Wait for the merged list to hold every remote agent.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if c, _, _ := hub.liveCount(); c == k*n {
			break
		}
		require.True(t, time.Now().Before(deadline), "hub never listed all %d remote agents", k*n)
		time.Sleep(200 * time.Millisecond)
	}

	clients := make([]*dashClient, m)
	for i := range clients {
		clients[i] = newDashClient(t, hub, remotes)
	}
	time.Sleep(6 * time.Second) // first coral_update + one tick before measuring

	// ── measurement window ──
	for _, r := range remotes {
		r.resetCounters()
	}
	for _, c := range clients {
		c.reset()
	}
	writes0 := hub.dbWrites()
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	cpu0 := processCPU()
	g0 := runtime.NumGoroutine()
	profPath := filepath.Join(outDir, fmt.Sprintf("hub_scale_K%d.pprof", k))
	pf, err := os.Create(profPath)
	require.NoError(t, err)
	require.NoError(t, pprof.StartCPUProfile(pf))
	start := time.Now()

	liveLat := hub.sampleLive(window, 500*time.Millisecond, k*n)

	pprof.StopCPUProfile()
	pf.Close()
	elapsed := time.Since(start)
	cpu1 := processCPU()
	runtime.ReadMemStats(&ms1)
	g1 := runtime.NumGoroutine()
	writes1 := hub.dbWrites()
	perMin := func(v float64) float64 { return v * float64(time.Minute) / float64(elapsed) }

	t.Logf("=== K=%d remotes x N=%d agents (%d remote sessions), M=%d dashboard clients, window %s ===", k, n, k*n, m, elapsed.Round(time.Second))
	t.Logf("hub GET /api/sessions/live: %s, body %s", liveLat.summary(), byteSize(liveLat.bytes))
	var fanout, updSizes, diffSizes []time.Duration
	var msgs, msgBytes int64
	for _, c := range clients {
		c.mu.Lock()
		fanout = append(fanout, c.markerLat...)
		updSizes = append(updSizes, c.updateSizes...)
		diffSizes = append(diffSizes, c.diffSizes...)
		msgs += c.msgs
		msgBytes += c.bytes
		c.mu.Unlock()
	}
	t.Logf("ws fan-out (remote diff sent -> each dashboard client receives it): %s", durSummary(fanout))
	t.Logf("ws messages per client: %.1f/min, %s/min; coral_diff size %s; coral_update size %s",
		perMin(float64(msgs))/float64(m), byteSize(int(perMin(float64(msgBytes))/float64(m))), sizeSummary(diffSizes), sizeSummary(updSizes))
	var reqs, rbytes, wsBytes, wsMsgs int64
	for _, r := range remotes {
		reqs += r.httpReqs.Load()
		rbytes += r.httpBytes.Load()
		wsBytes += r.wsBytes.Load()
		wsMsgs += r.wsMsgs.Load()
	}
	t.Logf("each remote serves (mean): %.1f HTTP req/min (%s/min) + %.1f ws msgs/min (%s/min); paths %s",
		perMin(float64(reqs))/float64(k), byteSize(int(perMin(float64(rbytes))/float64(k))),
		perMin(float64(wsMsgs))/float64(k), byteSize(int(perMin(float64(wsBytes))/float64(k))), remotes[0].pathCounts())
	t.Logf("hub DB writes (remote_servers UPDATEs): %.1f/min (%.2f per remote per min)", perMin(float64(writes1-writes0)), perMin(float64(writes1-writes0))/float64(k))
	t.Logf("process CPU (hub + fakes + clients): %.2f cores; allocs %.0f/s, %s/s; goroutines %d -> %d (baseline before hub %d)",
		(cpu1-cpu0).Seconds()/elapsed.Seconds(), float64(ms1.Mallocs-ms0.Mallocs)/elapsed.Seconds(), byteSize(int(float64(ms1.TotalAlloc-ms0.TotalAlloc)/elapsed.Seconds())), g0, g1, baseGoroutines)
	t.Logf("cpu profile: %s", profPath)
	logHubProfileTop(t, profPath)

	if fault {
		runFaultPhase(t, hub, remotes, clients, faultFor)
	}

	for _, c := range clients {
		c.close()
	}
	for _, r := range remotes {
		code, _ := hub.do("DELETE", "/api/servers/"+r.id, nil)
		require.Equal(t, http.StatusOK, code)
	}
	time.Sleep(12 * time.Second) // one registry refresh stops the workers
	afterRemove := runtime.NumGoroutine()
	hub.close()
	time.Sleep(time.Second)
	t.Logf("goroutines: after removing all remotes and clients %d, after hub shutdown %d (before hub %d)", afterRemove, runtime.NumGoroutine(), baseGoroutines)
}

// runFaultPhase makes one remote hang (HTTP slower than the hub's 10s request
// timeout, feed socket open but silent) and takes another offline, then checks
// that the hub's list stays fast and the healthy remotes keep flowing.
func runFaultPhase(t *testing.T, hub *scaleHub, remotes []*fakeRemote, clients []*dashClient, d time.Duration) {
	require.GreaterOrEqual(t, len(remotes), 3)
	slow, offline := remotes[0], remotes[1]
	healthy := remotes[2:]
	for _, c := range clients {
		c.reset()
	}
	g0 := runtime.NumGoroutine()
	slow.setSlow(15 * time.Second)
	offline.goOffline()
	statusAt := map[string]time.Duration{}
	start := time.Now()
	var mu sync.Mutex
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
			for _, r := range []*fakeRemote{slow, offline} {
				if st := hub.serverStatus(r.id); st != store.RemoteStatusOnline {
					mu.Lock()
					if _, ok := statusAt[r.id]; !ok {
						statusAt[r.id] = time.Since(start)
					}
					mu.Unlock()
				}
			}
		}
	}()
	liveLat := hub.sampleLive(d, 500*time.Millisecond, -1)
	close(stop)

	var fanout []time.Duration
	healthyIDs := map[string]bool{}
	for _, r := range healthy {
		healthyIDs[r.id] = true
	}
	var faultMarkers int
	for _, c := range clients {
		c.mu.Lock()
		for i, srv := range c.markerSrv {
			if healthyIDs[srv] {
				fanout = append(fanout, c.markerLat[i])
			} else {
				faultMarkers++
			}
		}
		c.mu.Unlock()
	}
	_, staleSlow, staleOffline := hub.liveStale(slow.id, offline.id)
	t.Logf("--- fault phase %s: %s slow (HTTP %s > 10s timeout, feed socket open but silent), %s offline ---", d, slow.id, slow.delay(), offline.id)
	t.Logf("hub GET /api/sessions/live during fault: %s", liveLat.summary())
	t.Logf("healthy remotes' fan-out during fault: %s (markers from faulty remotes seen: %d)", durSummary(fanout), faultMarkers)
	mu.Lock()
	t.Logf("time until hub marks %s non-online: %v; %s: %v; their sessions stale-flagged in list: slow=%v offline=%v",
		slow.id, statusAt[slow.id], offline.id, statusAt[offline.id], staleSlow, staleOffline)
	mu.Unlock()
	t.Logf("slow remote: %d HTTP requests started during fault (in flight now %d); goroutines %d -> %d",
		slow.httpReqs.Load(), slow.inFlight.Load(), g0, runtime.NumGoroutine())
	slow.setSlow(0)
	offline.goOnline()
	recovered := time.Now()
	for time.Since(recovered) < 60*time.Second {
		if hub.serverStatus(slow.id) == store.RemoteStatusOnline && hub.serverStatus(offline.id) == store.RemoteStatusOnline {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("recovery: both back online after %s", time.Since(recovered).Round(100*time.Millisecond))
}

// ── hub ─────────────────────────────────────────────────────────────

type scaleHub struct {
	*e2eHub
}

func newScaleHub(t *testing.T) *scaleHub {
	t.Helper()
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.HubMode = true
	cfg.WSPollIntervalS = 5 // production cadence
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	h := &e2eHub{t: t, dir: dir, cfg: cfg, db: db, backend: stack.backend, done: make(chan struct{}), env: &e2eEnv{logs: &lockedBuf{}}}
	h.srv = New(cfg, db, stack.backend, stack.terminal)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS qa_write_log (n INTEGER);
		CREATE TRIGGER IF NOT EXISTS qa_count_remote_writes AFTER UPDATE ON remote_servers BEGIN INSERT INTO qa_write_log VALUES (1); END;`)
	require.NoError(t, err)
	h.ts = httptest.NewUnstartedServer(h.srv.Router())
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	pprof.Do(ctx, pprof.Labels("role", "hub"), func(ctx context.Context) {
		h.ts.Start()
		go func() { defer close(h.done); h.srv.RunRemoteAgents(ctx) }()
	})
	return &scaleHub{h}
}

func (h *scaleHub) close() {
	h.e2eHub.close()
}

func (h *scaleHub) dbWrites() int64 {
	var n int64
	_ = h.db.Get(&n, "SELECT COUNT(*) FROM qa_write_log")
	return n
}

// liveCount returns the number of sessions in the hub's merged list.
func (h *scaleHub) liveCount() (int, int, error) {
	resp, err := http.Get(h.ts.URL + "/api/sessions/live")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var v struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0, len(b), nil
	}
	return len(v.Sessions), len(b), nil
}

// liveStale reports whether the given servers' sessions carry stale=true.
func (h *scaleHub) liveStale(ids ...string) (int, bool, bool) {
	resp, err := http.Get(h.ts.URL + "/api/sessions/live")
	if err != nil {
		return 0, false, false
	}
	defer resp.Body.Close()
	var v struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&v)
	stale := map[string]bool{}
	for _, s := range v.Sessions {
		if b, _ := s["stale"].(bool); b {
			stale[s["server"].(string)] = true
		}
	}
	return len(v.Sessions), stale[ids[0]], stale[ids[1]]
}

type latencies struct {
	ds    []time.Duration
	bytes int
	bad   int
}

func (l latencies) summary() string {
	s := durSummary(l.ds)
	if l.bad > 0 {
		s += fmt.Sprintf(" (%d responses with the wrong session count)", l.bad)
	}
	return s
}

// sampleLive GETs the hub's merged list every interval for d, from two
// concurrent pollers (two dashboards refreshing).
func (h *scaleHub) sampleLive(d, every time.Duration, want int) latencies {
	var mu sync.Mutex
	var out latencies
	end := time.Now().Add(d)
	var wg sync.WaitGroup
	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) {
				t0 := time.Now()
				c, size, err := h.liveCount()
				lat := time.Since(t0)
				mu.Lock()
				if err == nil {
					out.ds = append(out.ds, lat)
					out.bytes = size
					if want >= 0 && c != want {
						out.bad++
					}
				} else {
					out.bad++
				}
				mu.Unlock()
				time.Sleep(every)
			}
		}()
	}
	wg.Wait()
	return out
}

// ── fake remote ─────────────────────────────────────────────────────

var markerRE = regexp.MustCompile(`mk-(r\d+)-(\d+)`)

type fakeRemote struct {
	t   *testing.T
	id  string
	n   int
	ts  *httptest.Server
	mu  sync.Mutex
	ses []map[string]any
	seq int

	slowFor   atomic.Int64 // ns; >0 makes HTTP hang and the feed go silent
	httpReqs  atomic.Int64
	httpBytes atomic.Int64
	wsMsgs    atomic.Int64
	wsBytes   atomic.Int64
	inFlight  atomic.Int64
	paths     sync.Map // path -> *atomic.Int64
	conns     sync.Map // *websocket.Conn -> struct{}
	offline   atomic.Bool
}

func newFakeRemote(t *testing.T, id string, n int) *fakeRemote {
	r := &fakeRemote{t: t, id: id, n: n}
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("%s-%04d-0000-4000-8000-%012d", id, i, i)
		r.ses = append(r.ses, map[string]any{
			"agent_type": "claude", "awaiting_user": false, "board_is_orchestrator": i == 0, "board_job_title": fmt.Sprintf("Agent %02d", i),
			"board_project": "scale-team", "board_unread": i % 3, "branch": "feature/scale-test", "changed_file_count": i % 7,
			"commands": map[string]any{"clear": "/clear", "compress": "/compact"}, "context_pct": 42, "context_window": 200000,
			"display_name": fmt.Sprintf("Agent %02d", i), "done": false, "enrichment_pending": false, "first_prompt": "Implement the next task from the board and report back with evidence",
			"git_state": map[string]any{"ahead": 1, "behind": 0, "dirty": true}, "icon": nil, "log_path": "/tmp/claude_coral_" + sid + ".log",
			"name": "work", "name_color": nil, "not_started": false, "repo_name": "coral", "session_id": sid, "sleeping": false,
			"staleness_seconds": 3, "status": "Running tests", "stuck": false, "summary": "Working on the scale test harness",
			"tmux_session": "claude-" + sid, "waiting_for_input": false, "waiting_reason": nil, "waiting_summary": nil, "working": true,
			"working_directory": "/home/dev/src/coral",
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, req *http.Request) {
		r.serveJSON(w, req, map[string]any{"status": "ok", "version": "scale-fake"})
	})
	mux.HandleFunc("/api/sessions/live", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		b, _ := json.Marshal(r.ses)
		r.mu.Unlock()
		r.serveBytes(w, req, b)
	})
	mux.HandleFunc("/ws/coral", r.serveFeed)
	r.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.offline.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
				}
			}
			return
		}
		r.count(req.URL.Path)
		mux.ServeHTTP(w, req)
	}))
	pprof.Do(context.Background(), pprof.Labels("role", "remote"), func(context.Context) { r.ts.Start() })
	t.Cleanup(func() {
		r.conns.Range(func(c, _ any) bool { c.(*websocket.Conn).CloseNow(); return true })
		r.ts.CloseClientConnections()
		r.ts.Close()
	})
	return r
}

func (r *fakeRemote) url() string { return r.ts.URL }

func (r *fakeRemote) count(path string) {
	v, _ := r.paths.LoadOrStore(path, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

func (r *fakeRemote) pathCounts() string {
	var parts []string
	r.paths.Range(func(k, v any) bool {
		parts = append(parts, fmt.Sprintf("%s=%d", k, v.(*atomic.Int64).Load()))
		return true
	})
	sort.Strings(parts)
	return "[" + strings.Join(parts, " ") + "] (r00, cumulative)"
}

func (r *fakeRemote) resetCounters() {
	r.httpReqs.Store(0)
	r.httpBytes.Store(0)
	r.wsMsgs.Store(0)
	r.wsBytes.Store(0)
}

func (r *fakeRemote) delay() time.Duration { return time.Duration(r.slowFor.Load()) }
func (r *fakeRemote) setSlow(d time.Duration) {
	r.slowFor.Store(int64(d))
	r.httpReqs.Store(0)
}

func (r *fakeRemote) goOffline() {
	r.offline.Store(true)
	r.conns.Range(func(c, _ any) bool { c.(*websocket.Conn).CloseNow(); return true })
	r.ts.CloseClientConnections()
}

func (r *fakeRemote) goOnline() { r.offline.Store(false) }

func (r *fakeRemote) hang(req *http.Request) {
	if d := r.delay(); d > 0 {
		r.inFlight.Add(1)
		defer r.inFlight.Add(-1)
		select {
		case <-time.After(d):
		case <-req.Context().Done():
		}
	}
}

func (r *fakeRemote) serveJSON(w http.ResponseWriter, req *http.Request, v any) {
	b, _ := json.Marshal(v)
	r.serveBytes(w, req, b)
}

func (r *fakeRemote) serveBytes(w http.ResponseWriter, req *http.Request, b []byte) {
	r.httpReqs.Add(1)
	r.hang(req)
	w.Header().Set("Content-Type", "application/json")
	n, _ := w.Write(b)
	r.httpBytes.Add(int64(n))
}

// serveFeed sends a coral_update, then every 5s a coral_diff changing two
// sessions whose summary carries a marker with the send time.
func (r *fakeRemote) serveFeed(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	r.conns.Store(conn, struct{}{})
	defer r.conns.Delete(conn)
	defer conn.CloseNow()
	ctx := conn.CloseRead(req.Context())
	write := func(v any) error {
		b, _ := json.Marshal(v)
		r.wsMsgs.Add(1)
		r.wsBytes.Add(int64(len(b)))
		return conn.Write(ctx, websocket.MessageText, b)
	}
	r.mu.Lock()
	update := map[string]any{"type": "coral_update", "sessions": r.ses, "active_runs": []any{}}
	b, _ := json.Marshal(update)
	r.mu.Unlock()
	r.wsMsgs.Add(1)
	r.wsBytes.Add(int64(len(b)))
	if conn.Write(ctx, websocket.MessageText, b) != nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	lastSent := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if r.delay() > 0 {
			continue // hung remote: socket stays open, nothing is sent
		}
		if hubScaleIdle {
			if time.Since(lastSent) >= 15*time.Second {
				if write(map[string]any{"type": "coral_heartbeat"}) != nil {
					return
				}
				lastSent = time.Now()
			}
			continue
		}
		r.mu.Lock()
		var changed []map[string]any
		for j := 0; j < 2; j++ {
			s := r.ses[(r.seq*2+j)%r.n]
			c := make(map[string]any, len(s))
			for kk, v := range s {
				c[kk] = v
			}
			c["summary"] = fmt.Sprintf("mk-%s-%d", r.id, time.Now().UnixNano())
			c["staleness_seconds"] = r.seq % 60
			r.ses[(r.seq*2+j)%r.n] = c
			changed = append(changed, c)
		}
		r.seq++
		r.mu.Unlock()
		if write(map[string]any{"type": "coral_diff", "changed": changed}) != nil {
			return
		}
		lastSent = time.Now()
	}
}

// ── dashboard client ────────────────────────────────────────────────

type dashClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc

	mu          sync.Mutex
	msgs, bytes int64
	updateSizes []time.Duration // sizes stored as Duration for the shared summary helpers
	diffSizes   []time.Duration
	markerLat   []time.Duration
	markerSrv   []string
	seen        map[string]bool
}

func newDashClient(t *testing.T, hub *scaleHub, _ []*fakeRemote) *dashClient {
	ctx, cancel := context.WithCancel(context.Background())
	c := &dashClient{cancel: cancel, seen: map[string]bool{}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.ts.URL, "http")+"/ws/coral", nil)
	require.NoError(t, err)
	conn.SetReadLimit(64 << 20)
	c.conn = conn
	pprof.Do(ctx, pprof.Labels("role", "client"), func(ctx context.Context) {
		go func() {
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				now := time.Now()
				typ := "coral_diff"
				if bytes.Contains(data[:min(len(data), 40)], []byte(`"coral_update"`)) {
					typ = "coral_update"
				}
				c.mu.Lock()
				c.msgs++
				c.bytes += int64(len(data))
				if typ == "coral_update" {
					c.updateSizes = append(c.updateSizes, time.Duration(len(data)))
				} else {
					c.diffSizes = append(c.diffSizes, time.Duration(len(data)))
				}
				for _, mm := range markerRE.FindAllSubmatch(data, -1) {
					key := string(mm[0])
					if c.seen[key] {
						continue
					}
					c.seen[key] = true
					ns, _ := strconv.ParseInt(string(mm[2]), 10, 64)
					c.markerLat = append(c.markerLat, now.Sub(time.Unix(0, ns)))
					c.markerSrv = append(c.markerSrv, string(mm[1]))
				}
				c.mu.Unlock()
			}
		}()
	})
	return c
}

func (c *dashClient) reset() {
	c.mu.Lock()
	c.msgs, c.bytes = 0, 0
	c.diffSizes, c.markerLat, c.markerSrv = nil, nil, nil // updateSizes keeps the initial full update
	c.mu.Unlock()
}

func (c *dashClient) close() {
	c.cancel()
	c.conn.CloseNow()
}

// ── helpers ─────────────────────────────────────────────────────────

func durSummary(ds []time.Duration) string {
	if len(ds) == 0 {
		return "n=0"
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p := func(q float64) time.Duration { return s[int(q*float64(len(s)-1)+0.5)] }
	r := func(d time.Duration) time.Duration {
		if d < 10*time.Millisecond {
			return d.Round(10 * time.Microsecond)
		}
		return d.Round(time.Millisecond)
	}
	return fmt.Sprintf("n=%d p50=%s p95=%s max=%s", len(s), r(p(0.5)), r(p(0.95)), r(s[len(s)-1]))
}

func sizeSummary(ds []time.Duration) string {
	if len(ds) == 0 {
		return "n=0"
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p := func(q float64) int { return int(s[int(q*float64(len(s)-1)+0.5)]) }
	return fmt.Sprintf("n=%d p50=%s p95=%s max=%s", len(s), byteSize(p(0.5)), byteSize(p(0.95)), byteSize(int(s[len(s)-1])))
}

func byteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// logHubProfileTop prints the hub-only (role=hub) top functions by cumulative
// CPU, when the go tool is available.
func logHubProfileTop(t *testing.T, path string) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return
	}
	out, err := exec.Command(goBin, "tool", "pprof", "-top", "-cum", "-nodecount=40", "-tagfocus=role=hub", path).CombinedOutput()
	if err != nil {
		t.Logf("pprof: %v", err)
		return
	}
	var keep []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "coral/") || strings.Contains(line, "flat%") || strings.HasPrefix(strings.TrimSpace(line), "Showing") || strings.Contains(line, "encoding/json") || strings.Contains(line, "Duration:") {
			keep = append(keep, line)
		}
	}
	t.Logf("hub-only CPU (pprof -tagfocus=role=hub -cum):\n%s", strings.Join(keep, "\n"))
}

var hubScaleIdle = os.Getenv("CORAL_HUBSCALE_IDLE") == "1"

func hubScaleEnvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
