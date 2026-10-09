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
// session list; the poll keeps running to track status. When the feed cannot
// be established the poll data is used.

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
	"time"

	"nhooyr.io/websocket"

	"github.com/cdknorow/coral/internal/server/proxy"
	"github.com/cdknorow/coral/internal/store"
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
}

// RemoteSnapshot is a point-in-time copy of all remote data. Sessions are
// already tagged with "server" (and "stale" when applicable); the maps are
// private copies and safe to mutate shallowly.
type RemoteSnapshot struct {
	Servers  []RemoteServerStatus
	Sessions []map[string]any
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
}

type remoteState struct {
	srv      store.RemoteServer
	cancel   context.CancelFunc
	status   string
	sessions []map[string]any
	feedLive bool
	lastDB   time.Time // last SetStatus(online) write
	lastSig  string
}

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
}

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
	changed := false
	h.mu.Lock()
	seen := map[string]bool{}
	for _, rs := range list {
		seen[rs.ID] = true
		st := h.remotes[rs.ID]
		if st != nil && (st.srv.URL != rs.URL || st.srv.AllowPrivate != rs.AllowPrivate) && st.cancel != nil {
			st.cancel()
			st.cancel = nil
			st.sessions, st.feedLive, st.status = nil, false, ""
		}
		if st == nil {
			st = &remoteState{}
			h.remotes[rs.ID] = st
			changed = true
		}
		st.srv = rs
		if rs.Status == store.RemoteStatusKeyUnreadable {
			if st.cancel != nil {
				st.cancel()
				st.cancel = nil
			}
			if st.status != store.RemoteStatusKeyUnreadable {
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
	h.mu.Unlock()
	if changed {
		h.notify()
	}
}

// Snapshot returns the current merged remote view, ordered by server id.
func (h *RemoteAgentHub) Snapshot() RemoteSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
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
		snap.Servers = append(snap.Servers, RemoteServerStatus{ID: id, Label: label, Status: status})
		for _, s := range st.sessions {
			c := make(map[string]any, len(s)+2)
			for k, v := range s {
				c[k] = v
			}
			c["server"] = id
			if status != store.RemoteStatusOnline {
				c["stale"] = true
			} else {
				delete(c, "stale")
			}
			snap.Sessions = append(snap.Sessions, c)
		}
	}
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
	backoff := h.cfg.MinBackoff
	for {
		sessions, err := h.fetchLive(ctx, id)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if err == nil {
			h.recordOnline(ctx, id, sessions)
			backoff = h.cfg.MinBackoff
			wait = h.jitter(h.cfg.PollInterval)
		} else {
			h.recordFailure(ctx, id, err)
			wait = backoff
			if backoff *= 2; backoff > h.cfg.MaxBackoff {
				backoff = h.cfg.MaxBackoff
			}
		}
		if !sleepCtx(ctx, wait) {
			return
		}
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
	st.status = store.RemoteStatusOnline
	if !st.feedLive {
		if sig := sessionsSig(sessions); sig != st.lastSig || changed {
			changed = changed || sig != st.lastSig
			st.lastSig = sig
			st.sessions = sessions
		}
	}
	writeDB := changed || time.Since(st.lastDB) > 30*time.Second
	if writeDB {
		st.lastDB = time.Now()
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
	}
	h.mu.Lock()
	st, ok := h.current(id)
	if !ok {
		h.mu.Unlock()
		return
	}
	changed := st.status != status
	st.status = status
	st.feedLive = false
	st.lastDB = time.Time{}
	h.mu.Unlock()
	msg := err.Error()
	if status == store.RemoteStatusUnauthorized {
		msg = "remote rejected API key"
	}
	_ = h.reg.SetStatus(ctx, id, status, msg)
	if changed {
		h.notify()
	}
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
	conn, err := proxy.DialWebSocket(dctx, t, "/ws/coral", "", nil)
	cancel()
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)

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
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == -1 && ctx.Err() == nil {
				h.logger.Debug("remote feed dropped", "server", id)
			}
			return
		}
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
