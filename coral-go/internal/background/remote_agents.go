package background

// RemoteAgentHub keeps an in-memory view of the agents running on registered
// remote Coral servers, for the multi-server hub (docs/spec-multi-server-hub.md
// section 3).
//
// FRONTEND CONTRACT (what the hub's list and feed expose):
//   - every session object carries "server" (string): "local" for hub agents,
//     the server id for remote agents. Remote agents may also carry
//     "stale": true when their server is not online (last-known data).
//   - GET /api/sessions/live returns a plain JSON array when no remotes are
//     registered (unchanged from before). When at least one remote is
//     registered it returns {"sessions":[...], "servers":[{id,label,status}]}.
//   - /ws/coral coral_update / coral_diff messages follow the same rules: when
//     remotes exist they carry "servers" and every session has "server".
//     coral_diff removals of remote sessions are reported in "removed_remote":
//     [{"server":id,"key":session_id-or-name}]; "removed" only lists local keys.
//
// Each registered remote that is not key_unreadable gets its own goroutine that
// polls GET /api/sessions/live every ~5s (jittered, per-request timeout, backoff
// 1s..30s on failure) and a second goroutine holding an upstream /ws/coral feed
// socket. While the feed is connected its data supersedes the poll for the
// session list, so the poll only checks liveness with GET /api/health (the list
// is ~1 KB per agent and costs the remote a full build; health is ~40 bytes and
// static). The full list is fetched again when the feed drops, or when it has
// been quiet for FeedQuietRefresh (a remote whose handlers hang keeps a silent
// socket). The feed socket is pinged every FeedPingInterval (a keepalive, 30s)
// and dropped when two pings in a row go unanswered. A server shown online is
// shown unreachable only after its probes have failed for OfflineGrace, so a
// brief reconnect never flashes "offline" (#3649). When the feed cannot be
// established the poll data is used.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/cdknorow/coral/internal/store"
	proxy "github.com/cdknorow/coral/pkg/hubproxy"
)

// RemoteRegistry is the part of store.RemoteServerStore the hub needs.
type RemoteRegistry interface {
	List(ctx context.Context) ([]store.RemoteServer, error)
	SetStatus(ctx context.Context, id, status, errMsg string) error
}

// RemoteServerStatus is the per-server summary returned in "servers".
type RemoteServerStatus struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	// Shown in the sidebar hover card.
	URL       string `json:"url,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Agents    int    `json:"agents"`
	Version   string `json:"version,omitempty"`
}

// RemoteSnapshot is a point-in-time view of all remote data. Sessions are
// already tagged with "server" (and "stale" when applicable). A snapshot is
// built once per change and shared by every caller until the next one, so it
// is READ-ONLY: do not modify its slices or maps (copy an entry first).
type RemoteSnapshot struct {
	Servers  []RemoteServerStatus
	Sessions []map[string]any

	enc *snapshotEncoding // per-snapshot cache of the encoded sessions
}

type snapshotEncoding struct {
	once     sync.Once
	sessions []json.RawMessage
}

// SessionsJSON returns each of Sessions encoded as JSON (encoding/json, as
// the HTTP and WS writers use), computed once per snapshot.
func (s RemoteSnapshot) SessionsJSON() []json.RawMessage {
	encode := func() []json.RawMessage {
		out := make([]json.RawMessage, len(s.Sessions))
		for i, sess := range s.Sessions {
			out[i], _ = json.Marshal(sess)
		}
		return out
	}
	if s.enc == nil {
		return encode()
	}
	s.enc.once.Do(func() { s.enc.sessions = encode() })
	return s.enc.sessions
}

// Configured reports whether any remote is registered.
func (s RemoteSnapshot) Configured() bool { return len(s.Servers) > 0 }

// RemoteAgentSource is consumed by the session routes.
type RemoteAgentSource interface {
	Snapshot() RemoteSnapshot
	// Subscribe returns a channel that receives a signal (coalesced) whenever
	// remote data or status changes, and a cancel func.
	Subscribe() (<-chan struct{}, func())
}

// RemoteAgentHubConfig tunes timings; zero values use production defaults.
type RemoteAgentHubConfig struct {
	PollInterval    time.Duration // default 5s
	RequestTimeout  time.Duration // default 10s
	RegistryRefresh time.Duration // default 10s
	MinBackoff      time.Duration // default 1s
	MaxBackoff      time.Duration // default 30s
	// FeedQuietRefresh: while the feed is live the list is not polled, unless
	// no feed message arrived for this long. Default 30s.
	FeedQuietRefresh time.Duration
	// FeedPingInterval: how often the upstream feed socket is pinged; it is
	// dropped after two pings in a row go unanswered. Default 30s: a keepalive,
	// not the liveness check (the polls and the status grace are). The agent
	// list and status probes keep PollInterval (#3649).
	FeedPingInterval time.Duration
	// OfflineGrace: a server shown online is shown unreachable only after its
	// probes have failed for this long, so a connection that drops and comes
	// back (an SSH port forward, a slow link) never flashes "offline" (#3649).
	// A rejected key, a refused host or an unreadable key shows at once.
	// Default 20s; negative means none (every failure shows at once).
	OfflineGrace time.Duration
}

func (c *RemoteAgentHubConfig) fill() {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.RegistryRefresh <= 0 {
		c.RegistryRefresh = 10 * time.Second
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.FeedQuietRefresh <= 0 {
		c.FeedQuietRefresh = 30 * time.Second
	}
	if c.FeedPingInterval <= 0 {
		c.FeedPingInterval = 30 * time.Second
	}
	if c.OfflineGrace == 0 {
		c.OfflineGrace = 20 * time.Second
	}
}

type remoteState struct {
	srv       store.RemoteServer
	cancel    context.CancelFunc
	status    string
	sessions  []map[string]any
	feedLive  bool
	feedMsgAt time.Time // last message of any type received on the feed
	noHealth  bool      // remote predates /api/health (404): liveness uses the list
	lastDB    time.Time // last SetStatus write (online or failure)
	lastErr   string    // error message of the last failure write
	lastSig   string
	version   string // remote's reported version (best effort)
	// failingSince is when the current run of unreachable probes began while the
	// server was shown online (zero when none): within OfflineGrace it stays
	// online (#3649).
	failingSince time.Time
	versionAt    time.Time // when it was last fetched
	// wake asks the poll loop to probe now instead of after its wait, e.g.
	// after a proxied request to this remote failed (#3202).
	wake chan struct{}
}

func newRemoteState() *remoteState { return &remoteState{wake: make(chan struct{}, 1)} }

// kick wakes the poll loop (coalesced). Call with h.mu held.
func (st *remoteState) kick() {
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// rowNeedsWrite reports whether the registry row must be rewritten now: the
// status changed, the row says something else (another writer, such as Test or
// the proxy, put a different status there), or the periodic last_seen write is
// due. Call with h.mu held.
func (st *remoteState) rowNeedsWrite(changed bool, status string) bool {
	return changed || st.srv.Status != status || time.Since(st.lastDB) >= remoteSeenWriteEvery
}

// remoteSeenWriteEvery bounds the remote_servers writes for a remote whose
// status is unchanged: last_seen (and a repeated error) is refreshed at most
// this often, so the UI's "last contact" is accurate to about a minute. A
// status change is always written at once (#3142).
var remoteSeenWriteEvery = time.Minute

// RemoteAgentHub implements RemoteAgentSource.
type RemoteAgentHub struct {
	reg      RemoteRegistry
	resolver proxy.Resolver
	cfg      RemoteAgentHubConfig
	logger   *slog.Logger

	mu      sync.Mutex
	remotes map[string]*remoteState
	subs    map[int]chan struct{}
	nextSub int
	gen     atomic.Uint64   // bumped on every notify, before subscribers are signalled
	snap    *RemoteSnapshot // built on demand, cleared on every change
}

// Generation increases whenever subscribers are notified of a change, so a
// reader can tell whether a snapshot it holds predates the latest change.
func (h *RemoteAgentHub) Generation() uint64 { return h.gen.Load() }

// NewRemoteAgentHub creates the hub. Call Run to start it.
func NewRemoteAgentHub(reg RemoteRegistry, resolver proxy.Resolver, cfg RemoteAgentHubConfig) *RemoteAgentHub {
	cfg.fill()
	return &RemoteAgentHub{
		reg: reg, resolver: resolver, cfg: cfg,
		logger:  slog.Default().With("service", "remote_agents"),
		remotes: map[string]*remoteState{},
		subs:    map[int]chan struct{}{},
	}
}

// Run reconciles the registry until ctx ends.
func (h *RemoteAgentHub) Run(ctx context.Context) {
	h.reconcile(ctx)
	t := time.NewTicker(h.cfg.RegistryRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for _, st := range h.remotes {
				if st.cancel != nil {
					st.cancel()
				}
			}
			h.mu.Unlock()
			proxy.CloseIdleConnections() // no keep-alive connections left behind
			return
		case <-t.C:
			h.reconcile(ctx)
		}
	}
}

// reconcile starts, restarts and stops per-remote workers to match the registry.
func (h *RemoteAgentHub) reconcile(ctx context.Context) {
	list, err := h.reg.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			h.logger.Warn("remote registry read failed", "error", err)
		}
		return
	}
	// A row marked key_unreadable stops its worker only if the key really cannot
	// be read now. Another writer (the proxy) may have put that status there, and
	// a stopped worker would otherwise stay stopped until Test (#3202). Checked
	// outside the lock: it reads and decrypts the key.
	keyUnreadable := map[string]bool{}
	for _, rs := range list {
		if rs.Status == store.RemoteStatusKeyUnreadable {
			_, rerr := h.resolver.Resolve(ctx, rs.ID)
			keyUnreadable[rs.ID] = errors.Is(rerr, proxy.ErrKeyUnreadable)
		}
	}
	changed := false
	h.mu.Lock()
	seen := map[string]bool{}
	for _, rs := range list {
		seen[rs.ID] = true
		st := h.remotes[rs.ID]
		if st != nil && (st.srv.URL != rs.URL || st.srv.AllowPrivate != rs.AllowPrivate) && st.cancel != nil {
			st.cancel()
			st.cancel = nil
			st.sessions, st.feedLive, st.status, st.noHealth = nil, false, "", false
		}
		if st == nil {
			st = newRemoteState()
			h.remotes[rs.ID] = st
			changed = true
		}
		st.srv = rs
		if rs.Status == store.RemoteStatusKeyUnreadable && keyUnreadable[rs.ID] {
			if st.cancel != nil {
				st.cancel()
				st.cancel = nil
			}
			if st.status != store.RemoteStatusKeyUnreadable {
				h.logTransition(rs.ID, st.status, store.RemoteStatusKeyUnreadable, "stored key cannot be decrypted")
				st.status, st.feedLive = store.RemoteStatusKeyUnreadable, false
				changed = true
			}
			continue
		}
		if st.cancel == nil {
			if st.status == store.RemoteStatusKeyUnreadable || st.status == "" {
				st.status = rs.Status
				if st.status == "" || st.status == store.RemoteStatusKeyUnreadable {
					st.status = store.RemoteStatusUnknown
				}
			}
			wctx, cancel := context.WithCancel(ctx)
			st.cancel = cancel
			go h.pollLoop(wctx, rs.ID)
			go h.feedLoop(wctx, rs.ID)
			changed = true
		}
	}
	for id, st := range h.remotes {
		if !seen[id] {
			if st.cancel != nil {
				st.cancel()
			}
			delete(h.remotes, id)
			changed = true
		}
	}
	h.snap = nil // the registry rows (last seen, last error, label) are in the snapshot
	h.mu.Unlock()
	if changed {
		h.notify()
	}
}

// Snapshot returns the current merged remote view, ordered by server id.
func (h *RemoteAgentHub) Snapshot() RemoteSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.snap != nil {
		return *h.snap
	}
	ids := make([]string, 0, len(h.remotes))
	for id := range h.remotes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var snap RemoteSnapshot
	for _, id := range ids {
		st := h.remotes[id]
		status := st.status
		if status == "" {
			status = store.RemoteStatusUnknown
		}
		label := st.srv.Label
		if label == "" {
			label = id
		}
		entry := RemoteServerStatus{ID: id, Label: label, Status: status, URL: st.srv.URL, Agents: countUnarchived(st.sessions), Version: st.version}
		if st.srv.LastSeen != nil {
			entry.LastSeen = *st.srv.LastSeen
		}
		if st.srv.LastError != nil && status != store.RemoteStatusOnline {
			entry.LastError = *st.srv.LastError
		}
		snap.Servers = append(snap.Servers, entry)
		for _, s := range st.sessions {
			c := make(map[string]any, len(s)+2)
			for k, v := range s {
				c[k] = v
			}
			c["server"] = id
			// Always explicit: a dashboard merges each diff into the session it
			// has and keeps keys a diff omits, so a deleted "stale" would leave a
			// recovered agent stale until a reload (#3159).
			c["stale"] = status != store.RemoteStatusOnline
			snap.Sessions = append(snap.Sessions, c)
		}
	}
	snap.enc = &snapshotEncoding{}
	h.snap = &snap
	return snap
}

// Subscribe implements RemoteAgentSource.
func (h *RemoteAgentHub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	id := h.nextSub
	h.nextSub++
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

func (h *RemoteAgentHub) notify() {
	// Drop the cached snapshot before the generation moves, so a reader that
	// sees the new generation also gets data at least that new.
	h.mu.Lock()
	h.snap = nil
	h.mu.Unlock()
	h.gen.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *RemoteAgentHub) jitter(d time.Duration) time.Duration {
	return d - d/5 + time.Duration(rand.Int63n(int64(d)/5*2+1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (h *RemoteAgentHub) current(id string) (*remoteState, bool) {
	st, ok := h.remotes[id]
	return st, ok
}

// pollLoop polls one remote until ctx ends.
func (h *RemoteAgentHub) pollLoop(ctx context.Context, id string) {
	h.mu.Lock()
	st, ok := h.current(id)
	h.mu.Unlock()
	if !ok {
		return
	}
	wake := st.wake
	backoff := h.cfg.MinBackoff
	for {
		probed := time.Now()
		var sessions []map[string]any
		var err error
		feedFresh := h.feedCarriesList(id)
		if feedFresh {
			// The feed supplies the list; only check that the remote answers.
			if err = h.checkHealth(ctx, id); errors.Is(err, errNoHealthEndpoint) {
				// An older remote: fall back to the list for good, never
				// mark a working remote unreachable.
				h.mu.Lock()
				if st, ok := h.current(id); ok {
					st.noHealth = true
				}
				h.mu.Unlock()
				feedFresh = false
			}
		}
		if !feedFresh {
			sessions, err = h.fetchLive(ctx, id)
		}
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if err == nil {
			if feedFresh {
				h.recordAlive(ctx, id)
			} else {
				h.recordOnline(ctx, id, sessions)
				h.refreshVersion(ctx, id)
			}
			backoff = h.cfg.MinBackoff
			wait = h.jitter(h.cfg.PollInterval)
		} else {
			h.recordFailure(ctx, id, err)
			wait = backoff
			if backoff *= 2; backoff > h.cfg.MaxBackoff {
				backoff = h.cfg.MaxBackoff
			}
		}
		if !h.waitOrWake(ctx, wake, wait, probed) {
			return
		}
	}
}

// waitOrWake waits for d, or less if the loop is kicked (a proxied request to
// the remote failed), so the next probe settles the status at once (#3202).
// Probes stay at least MinBackoff apart, so a burst of failures cannot turn
// into a burst of probes. It returns false when ctx ends.
func (h *RemoteAgentHub) waitOrWake(ctx context.Context, wake <-chan struct{}, d time.Duration, probed time.Time) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case <-wake:
		return sleepCtx(ctx, h.cfg.MinBackoff-time.Since(probed))
	}
}

// feedCarriesList reports whether the remote's feed is connected and has
// delivered a message within FeedQuietRefresh, so polling the list would only
// fetch data the feed already supersedes.
func (h *RemoteAgentHub) feedCarriesList(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.current(id)
	return ok && st.feedLive && !st.noHealth && time.Since(st.feedMsgAt) < h.cfg.FeedQuietRefresh
}

// errNoHealthEndpoint means the remote answered /api/health with 404: it
// predates the endpoint, which says nothing about whether it is up.
var errNoHealthEndpoint = errors.New("remote has no /api/health endpoint")

// checkHealth GETs /api/health and records the reported version. It is the
// liveness check used while the feed carries the list.
func (h *RemoteAgentHub) checkHealth(ctx context.Context, id string) error {
	t, err := h.resolver.Resolve(ctx, id)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, h.cfg.RequestTimeout)
	defer cancel()
	resp, err := proxy.Get(rctx, t, "/api/health", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return errNoHealthEndpoint
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return errors.New("remote returned HTTP " + resp.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return errors.New("remote returned an invalid health response")
	}
	if body.Version != "" {
		h.mu.Lock()
		changed := false
		if st, ok := h.current(id); ok && st.version != body.Version {
			st.version, st.versionAt, changed = body.Version, time.Now(), true
		}
		h.mu.Unlock()
		if changed {
			h.notify()
		}
	}
	return nil
}

// recordAlive is recordOnline for a liveness check that carries no list: the
// status and the periodic last_seen write, sessions untouched.
func (h *RemoteAgentHub) recordAlive(ctx context.Context, id string) {
	h.mu.Lock()
	st, ok := h.current(id)
	if !ok {
		h.mu.Unlock()
		return
	}
	changed := st.status != store.RemoteStatusOnline
	if changed {
		h.logTransition(id, st.status, store.RemoteStatusOnline, "")
	}
	st.status = store.RemoteStatusOnline
	st.failingSince = time.Time{}
	writeDB := st.rowNeedsWrite(changed, store.RemoteStatusOnline)
	if writeDB {
		st.lastDB, st.srv.Status = time.Now(), store.RemoteStatusOnline
	}
	h.mu.Unlock()
	if writeDB {
		_ = h.reg.SetStatus(ctx, id, store.RemoteStatusOnline, "")
	}
	if changed {
		h.notify()
	}
}

// refreshVersion reads the remote's version from /api/health at most once a
// minute. It is for display only, so failures are ignored.
func (h *RemoteAgentHub) refreshVersion(ctx context.Context, id string) {
	h.mu.Lock()
	st, ok := h.current(id)
	due := ok && time.Since(st.versionAt) > time.Minute
	if due {
		st.versionAt = time.Now()
	}
	h.mu.Unlock()
	if !due {
		return
	}
	t, err := h.resolver.Resolve(ctx, id)
	if err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, h.cfg.RequestTimeout)
	defer cancel()
	resp, err := proxy.Get(rctx, t, "/api/health", "")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) != nil || body.Version == "" {
		return
	}
	h.mu.Lock()
	changed := false
	if st, ok := h.current(id); ok && st.version != body.Version {
		st.version, changed = body.Version, true
	}
	h.mu.Unlock()
	if changed {
		h.notify()
	}
}

func (h *RemoteAgentHub) fetchLive(ctx context.Context, id string) ([]map[string]any, error) {
	t, err := h.resolver.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, h.cfg.RequestTimeout)
	defer cancel()
	resp, err := proxy.Get(rctx, t, "/api/sessions/live", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, errors.New("remote returned HTTP " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return parseLiveList(body)
}

// parseLiveList accepts the plain array form and the {"sessions":[...]} form
// (a remote that is itself a hub), dropping entries that belong to servers
// other than the remote itself.
func parseLiveList(body []byte) ([]map[string]any, error) {
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		var obj struct {
			Sessions []map[string]any `json:"sessions"`
		}
		if err2 := json.Unmarshal(body, &obj); err2 != nil {
			return nil, errors.New("remote returned an invalid session list")
		}
		list = obj.Sessions
	}
	return filterLocalOnly(list), nil
}

func filterLocalOnly(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, s := range in {
		if sv, _ := s["server"].(string); sv != "" && sv != "local" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func sessionsSig(s []map[string]any) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (h *RemoteAgentHub) recordOnline(ctx context.Context, id string, sessions []map[string]any) {
	h.mu.Lock()
	st, ok := h.current(id)
	if !ok {
		h.mu.Unlock()
		return
	}
	changed := st.status != store.RemoteStatusOnline
	statusChanged := changed
	if changed {
		h.logTransition(id, st.status, store.RemoteStatusOnline, "")
	}
	st.status = store.RemoteStatusOnline
	st.failingSince = time.Time{}
	if !st.feedLive {
		if sig := sessionsSig(sessions); sig != st.lastSig || changed {
			changed = changed || sig != st.lastSig
			st.lastSig = sig
			st.sessions = sessions
		}
	}
	// The row records status and last_seen, not sessions: a session change
	// does not need a write (#3142).
	writeDB := st.rowNeedsWrite(statusChanged, store.RemoteStatusOnline)
	if writeDB {
		st.lastDB, st.srv.Status = time.Now(), store.RemoteStatusOnline
	}
	h.mu.Unlock()
	if writeDB {
		_ = h.reg.SetStatus(ctx, id, store.RemoteStatusOnline, "")
	}
	if changed {
		h.notify()
	}
}

func (h *RemoteAgentHub) recordFailure(ctx context.Context, id string, err error) {
	status := store.RemoteStatusUnreachable
	switch {
	case errors.Is(err, proxy.ErrRemoteRejected):
		status = store.RemoteStatusUnauthorized
	case errors.Is(err, proxy.ErrKeyUnreadable):
		status = store.RemoteStatusKeyUnreadable
	case errors.Is(err, proxy.ErrHostRefused):
		status = store.RemoteStatusHostRefused
	}
	h.mu.Lock()
	st, ok := h.current(id)
	if !ok {
		h.mu.Unlock()
		return
	}
	if h.withinOfflineGrace(st, status) {
		h.mu.Unlock()
		h.logger.Debug("remote probe failed; still within the offline grace", "server", id, "error", err)
		return
	}
	msg := err.Error()
	if status == store.RemoteStatusUnauthorized {
		msg = "remote rejected API key"
	}
	changed := st.status != status
	if changed {
		h.logTransition(id, st.status, status, msg)
	}
	st.status = status
	st.feedLive = false
	// A repeated failure with the same error is written at most once a minute;
	// a new status or a different error is written at once (#3142).
	writeDB := st.rowNeedsWrite(changed, status) || msg != st.lastErr
	if writeDB {
		st.lastDB, st.srv.Status = time.Now(), status
		st.lastErr = msg
	}
	h.mu.Unlock()
	if writeDB {
		_ = h.reg.SetStatus(ctx, id, status, msg)
	}
	if changed {
		h.notify()
	}
}

// Refresh takes a server's status from an edit or a Test that just checked it (#3649): the
// worker's own view, which is what the dashboard shows, becomes that status at once instead of
// after its next probe (up to MaxBackoff away after a run of failures), and the worker probes
// again now.
func (h *RemoteAgentHub) Refresh(id, status string) {
	h.mu.Lock()
	st, ok := h.current(id)
	if ok {
		if st.status != status {
			h.logTransition(id, st.status, status, "the server was edited or tested")
		}
		st.status, st.srv.Status = status, status
		if status == store.RemoteStatusOnline {
			st.lastErr = ""
		}
		h.snap = nil
		st.kick()
	}
	h.mu.Unlock()
	if ok {
		h.notify()
	}
}

// withinOfflineGrace reports whether an unreachable result is kept from a server
// shown online because its failures haven't lasted OfflineGrace yet (#3649). It
// starts the run on the first failure. Any other status (a rejected key, a
// refused host, an unreadable key) is never held back. Called with h.mu held.
func (h *RemoteAgentHub) withinOfflineGrace(st *remoteState, status string) bool {
	if h.cfg.OfflineGrace < 0 || status != store.RemoteStatusUnreachable || st.status != store.RemoteStatusOnline {
		return false
	}
	now := time.Now()
	if st.failingSince.IsZero() {
		st.failingSince = now
	}
	return now.Sub(st.failingSince) < h.cfg.OfflineGrace
}

// ReportFailure records a failure seen outside the worker, i.e. a proxied
// dashboard request to the remote (#3202). The worker's own status changes with
// the registry row, so the two never disagree, and the worker probes again at
// once: a remote that is fine is back to online within a probe, instead of the
// row staying failed until the next periodic write or a Test. A key_unreadable
// report does not stop the worker; its own probe decides. It returns false if
// no worker runs for id, and the caller records the status itself.
func (h *RemoteAgentHub) ReportFailure(ctx context.Context, id, status, msg string) bool {
	h.mu.Lock()
	st, ok := h.current(id)
	if !ok || st.cancel == nil {
		h.mu.Unlock()
		return false
	}
	if h.withinOfflineGrace(st, status) {
		st.kick() // the worker's probe decides once the grace is over
		h.mu.Unlock()
		return true
	}
	changed := st.status != status
	if changed {
		h.logTransition(id, st.status, status, msg+" (proxied request)")
	}
	st.status = status
	st.lastDB, st.lastErr, st.srv.Status = time.Now(), msg, status
	st.kick()
	h.mu.Unlock()
	_ = h.reg.SetStatus(ctx, id, status, msg)
	// A probe may have finished between the unlock and the write: it wrote its own
	// status (say online) and recorded that the row says so, and this write then put
	// the failure back in the row. Record what the row really says now, so the next
	// probe sees the mismatch and corrects the row at once.
	h.mu.Lock()
	if st, ok := h.current(id); ok && st.status != status {
		st.srv.Status = status
		st.kick()
	}
	h.mu.Unlock()
	if changed {
		h.notify()
	}
	return true
}

// logTransition logs one line per status change. msg is an error message the
// registry also stores, never key material.
func (h *RemoteAgentHub) logTransition(id, from, to, msg string) {
	if from == "" {
		from = store.RemoteStatusUnknown
	}
	if msg == "" {
		h.logger.Info("remote status changed", "server", id, "from", from, "to", to)
		return
	}
	h.logger.Info("remote status changed", "server", id, "from", from, "to", to, "error", msg)
}

// feedLoop maintains the upstream /ws/coral socket for one remote while the
// remote is online, with reconnect backoff.
func (h *RemoteAgentHub) feedLoop(ctx context.Context, id string) {
	backoff := h.cfg.MinBackoff
	for {
		h.mu.Lock()
		st, ok := h.current(id)
		online := ok && st.status == store.RemoteStatusOnline
		h.mu.Unlock()
		if !ok {
			return
		}
		if online {
			started := time.Now()
			h.runFeed(ctx, id)
			if ctx.Err() != nil {
				return
			}
			if time.Since(started) > 10*time.Second {
				backoff = h.cfg.MinBackoff
			}
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > h.cfg.MaxBackoff {
			backoff = h.cfg.MaxBackoff
		}
	}
}

func feedKey(s map[string]any) string {
	if sid, _ := s["session_id"].(string); sid != "" {
		return sid
	}
	name, _ := s["name"].(string)
	return name
}

func (h *RemoteAgentHub) runFeed(ctx context.Context, id string) {
	t, err := h.resolver.Resolve(ctx, id)
	if err != nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, h.cfg.RequestTimeout)
	// feed=hub: this is a machine feed, not a dashboard, so the remote keeps
	// its UI notifications for its own dashboards (see routes WSCoral).
	conn, err := proxy.DialWebSocket(dctx, t, "/ws/coral", "feed=hub", nil)
	cancel()
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)
	fctx, stopFeed := context.WithCancel(ctx)
	defer stopFeed()
	if p, ok := conn.(feedPinger); ok {
		go h.pingFeed(fctx, conn, p)
	}

	feed := map[string]map[string]any{}
	order := []string{}
	defer func() {
		h.mu.Lock()
		if st, ok := h.current(id); ok {
			st.feedLive = false
		}
		h.mu.Unlock()
	}()
	for {
		_, data, err := conn.Read(fctx)
		if err != nil {
			if websocket.CloseStatus(err) == -1 && ctx.Err() == nil {
				h.logger.Debug("remote feed dropped", "server", id)
			}
			return
		}
		// Any message (coral_heartbeat included) shows the remote's feed loop
		// is alive, so the list does not need re-polling.
		h.mu.Lock()
		if st, ok := h.current(id); ok {
			st.feedMsgAt = time.Now()
		}
		h.mu.Unlock()
		var msg struct {
			Type     string           `json:"type"`
			Sessions []map[string]any `json:"sessions"`
			Changed  []map[string]any `json:"changed"`
			Removed  []string         `json:"removed"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "coral_update":
			feed = map[string]map[string]any{}
			order = order[:0]
			for _, s := range filterLocalOnly(msg.Sessions) {
				if k := feedKey(s); k != "" {
					feed[k] = s
					order = append(order, k)
				}
			}
		case "coral_diff":
			for _, s := range filterLocalOnly(msg.Changed) {
				k := feedKey(s)
				if k == "" {
					continue
				}
				if _, exists := feed[k]; !exists {
					order = append(order, k)
				}
				feed[k] = s
			}
			for _, k := range msg.Removed {
				delete(feed, k)
			}
		default:
			continue
		}
		sessions := make([]map[string]any, 0, len(feed))
		kept := order[:0]
		seen := map[string]bool{}
		for _, k := range order {
			if s, ok := feed[k]; ok && !seen[k] {
				seen[k] = true
				kept = append(kept, k)
				sessions = append(sessions, s)
			}
		}
		order = kept
		sig := sessionsSig(sessions)
		h.mu.Lock()
		st, ok := h.current(id)
		if !ok {
			h.mu.Unlock()
			return
		}
		changed := !st.feedLive || sig != st.lastSig
		st.feedLive = true
		st.lastSig = sig
		st.sessions = sessions
		h.mu.Unlock()
		if changed {
			h.notify()
		}
	}
}

// feedPinger is implemented by sockets that support websocket pings
// (*websocket.Conn). Tunnel sockets do not; their feed relies on the quiet
// refresh and the remote closing the stream.
type feedPinger interface {
	Ping(ctx context.Context) error
}

// pingFeed pings the upstream feed every FeedPingInterval and drops the socket when
// two pings in a row are not answered within RequestTimeout (a half-open
// connection or a remote that stopped reading; one slow answer over a port
// forward is tolerated, #3649). Dropping it only ends feedLive: the poll goes
// back to fetching the list, which decides the remote's status.
func (h *RemoteAgentHub) pingFeed(ctx context.Context, conn proxy.WSConn, p feedPinger) {
	missed := 0
	t := time.NewTicker(h.cfg.FeedPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, h.cfg.RequestTimeout)
		err := p.Ping(pctx)
		cancel()
		if err == nil {
			missed = 0
			continue
		}
		if missed++; missed < 2 && ctx.Err() == nil {
			continue
		}
		if ctx.Err() == nil {
			h.logger.Debug("remote feed ping failed twice; dropping feed", "error", err)
		}
		conn.CloseNow()
		return
	}
}

// NewRemoteSnapshot builds a snapshot from already tagged sessions, for
// RemoteAgentSource implementations other than the hub (tests, fakes). Like
// the hub's, it caches its encoded sessions and must be treated as read-only.
func NewRemoteSnapshot(servers []RemoteServerStatus, sessions []map[string]any) RemoteSnapshot {
	return RemoteSnapshot{Servers: servers, Sessions: sessions, enc: &snapshotEncoding{}}
}

// countUnarchived is the server's agent count: archived agents are kept out of
// it, as they are out of the agent list (#3030).
func countUnarchived(sessions []map[string]any) int {
	n := 0
	for _, s := range sessions {
		if s["archived"] != true {
			n++
		}
	}
	return n
}
