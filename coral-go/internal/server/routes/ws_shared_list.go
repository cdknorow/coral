package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/background"
)

// Every /ws/coral connection used to rebuild the session list on its own tick
// (tmux discovery, a dozen queries, per-agent log and transcript reads) and
// then marshal every local and remote session to diff it against what it sent
// last. With several tabs, popouts and hub pollers connected, all of that ran
// once per client per tick. wsTickCache builds and serializes once and every
// connection diffs the shared result by comparing strings.

// wsListMaxAge bounds how old a shared part may be. It is well under the 5s
// tick and the 1s remote-change debounce, so a client never sees data older
// than it would have built itself on a slightly earlier tick.
//
// It is atomic only so a test can shorten it with a matching poll interval and check the same sharing in a fraction of the time (#4914): the
// server's connection goroutines are not waited for at shutdown, so a plain var restored by the test would race with their last read. The
// server never changes it.
var wsListMaxAgeNs atomic.Int64

// wsListMaxAgeDefault is the production age; tests read it where package-level vars are initialised (before init runs).
const wsListMaxAgeDefault = 750 * time.Millisecond

func init() { wsListMaxAgeNs.Store(int64(wsListMaxAgeDefault)) }

func wsListMaxAge() time.Duration { return time.Duration(wsListMaxAgeNs.Load()) }

// wsListBuildTimeout bounds a shared build, which no longer runs under any one
// client's request context.
const wsListBuildTimeout = 30 * time.Second

// wsTick is one tick's data for every connection. It is read-only once
// published: connections only read the maps and encode them.
type wsTick struct {
	sessions    []map[string]any  // the coral_update list: local, then remote
	current     map[string]string // diff key -> serialized entry
	byKey       map[string]map[string]any
	servers     []background.RemoteServerStatus
	hasRemotes  bool
	serversJSON string
	activeRuns  []map[string]any
	runsJSON    string
}

// wsLocalPart is this server's sessions and job runs, serialized.
type wsLocalPart struct {
	builtAt    time.Time
	kickGen    uint64 // wsTickCache.kickGen when the build started
	hasRemotes bool   // whether entries carry server: "local"
	err        error
	sessions   []map[string]any
	keys       []string // diff key per session; "" is sent but not diffed
	serialized []string
	activeRuns []map[string]any
	runsJSON   string
	reused     int // rows whose serialized form came from the previous tick (#4308)
}

// wsRemotePart is the hub's remote view, serialized.
type wsRemotePart struct {
	builtAt     time.Time
	gen         uint64
	genOK       bool
	snap        background.RemoteSnapshot
	sessions    []map[string]any // remote sessions that have a feed key
	keys        []string
	serialized  []string
	serversJSON string
}

type wsTickCache struct {
	mu       sync.Mutex
	local    *wsLocalPart
	remote   *wsRemotePart
	tick     *wsTick
	tickFrom [2]any        // the parts tick was assembled from
	building chan struct{} // closed when the build in progress finishes
	kickGen  uint64        // bumped by kickWS: a local part from before it is stale
}

// remoteGeneration reports the remote hub's change counter, if it has one.
func (h *SessionsHandler) remoteGeneration() (uint64, bool) {
	if h.remoteAgents == nil {
		return 0, true
	}
	if g, ok := h.remoteAgents.(interface{ Generation() uint64 }); ok {
		return g.Generation(), true
	}
	return 0, false
}

// sharedWSTick returns the data for one /ws/coral tick, reusing parts that
// are fresh: the local part for wsListMaxAge, the remote part while it is
// that young and the hub has not signalled a change since it was taken.
func (h *SessionsHandler) sharedWSTick(r *http.Request) (*wsTick, error) {
	c := &h.wsList
	for {
		gen, genOK := h.remoteGeneration()
		c.mu.Lock()
		remoteFresh := c.remote != nil && genOK && c.remote.genOK && c.remote.gen == gen && time.Since(c.remote.builtAt) < wsListMaxAge()
		localFresh := c.local != nil && c.local.kickGen == c.kickGen && time.Since(c.local.builtAt) < wsListMaxAge()
		if remoteFresh && localFresh && c.local.hasRemotes == c.remote.snap.Configured() {
			if c.tick == nil || c.tickFrom != [2]any{c.local, c.remote} {
				c.tick = assembleWSTick(c.local, c.remote)
				c.tickFrom = [2]any{c.local, c.remote}
			}
			tick, err := c.tick, c.local.err
			c.mu.Unlock()
			return tick, err
		}
		if wait := c.building; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		done := make(chan struct{})
		c.building = done
		local, remote, kickGen := c.local, c.remote, c.kickGen
		c.mu.Unlock()
		return h.buildSharedWSTick(r, done, local, remote, !localFresh, !remoteFresh, gen, genOK, kickGen)
	}
}

// buildSharedWSTick rebuilds the stale parts and publishes the tick. However
// it exits, the build it claimed (done) is released and waiting connections
// wake up: otherwise one panic during a build would block every /ws/coral
// connection for good. A panic is logged and returned as an error, and nothing
// is published, so the previous parts stay in place.
func (h *SessionsHandler) buildSharedWSTick(r *http.Request, done chan struct{}, local *wsLocalPart, remote *wsRemotePart, localStale, remoteStale bool, gen uint64, genOK bool, kickGen uint64) (tick *wsTick, err error) {
	c := &h.wsList
	published := false
	defer func() {
		if p := recover(); p != nil {
			slog.Error("ws/coral session list build panicked", "panic", p, "stack", string(debug.Stack()))
			tick, err = nil, fmt.Errorf("session list build panicked: %v", p)
		}
		if !published {
			c.mu.Lock()
			if c.building == done {
				c.building = nil
			}
			c.mu.Unlock()
			close(done)
		}
	}()

	if remoteStale {
		remote = h.buildWSRemotePart(gen, genOK)
	}
	if localStale || local.hasRemotes != remote.snap.Configured() {
		local = h.buildWSLocalPart(r, remote.snap.Configured(), local)
		local.kickGen = kickGen // a kick during this build leaves it stale for the next request
	}

	c.mu.Lock()
	c.local, c.remote, c.building = local, remote, nil
	c.tick = assembleWSTick(local, remote)
	c.tickFrom = [2]any{local, remote}
	tick = c.tick
	c.mu.Unlock()
	published = true
	close(done)
	return tick, local.err
}

// buildWSLocalPart builds this server's part of the list. prev is the part built before it (nil for
// the first): a row that comes out exactly as it did then keeps the string it was serialized to
// (#4308). The row is still built from scratch every tick, from every input, so nothing can go stale;
// what is skipped is json.Marshal of a row that did not change, the largest allocator of a tick.
// "Exactly as it did" is sameJSONValue over the whole built row: any value that is not a type it
// knows counts as changed, and the row is serialized again.
func (h *SessionsHandler) buildWSLocalPart(r *http.Request, hasRemotes bool, prev *wsLocalPart) *wsLocalPart {
	if h.wsBuildHook != nil {
		h.wsBuildHook()
	}
	ctx, cancel := context.WithTimeout(context.Background(), wsListBuildTimeout)
	defer cancel()
	sessions, err := h.buildSessionListForWS(r.Clone(ctx))
	p := &wsLocalPart{hasRemotes: hasRemotes, err: err, sessions: sessions}
	var before map[string]int // key -> index in prev
	if prev != nil && prev.err == nil && err == nil {
		before = make(map[string]int, len(prev.keys))
		for i, k := range prev.keys {
			if k != "" {
				before[k] = i
			}
		}
	}
	for _, s := range sessions {
		if hasRemotes {
			s["server"] = "local"
		}
		key := ""
		if sid, ok := s["session_id"].(string); ok && sid != "" {
			key = sid
		} else if name, ok := s["name"].(string); ok {
			key = name
		}
		serialized := ""
		if key != "" {
			if i, ok := before[key]; ok && sameJSONValue(prev.sessions[i], s) {
				serialized = prev.serialized[i]
				p.reused++
			} else {
				b, _ := json.Marshal(s)
				serialized = string(b)
			}
		}
		p.keys = append(p.keys, key)
		p.serialized = append(p.serialized, serialized)
	}
	p.activeRuns = h.getActiveRuns(ctx)
	runs, _ := json.Marshal(p.activeRuns)
	p.runsJSON = string(runs)
	p.builtAt = time.Now()
	return p
}

func (h *SessionsHandler) buildWSRemotePart(gen uint64, genOK bool) *wsRemotePart {
	p := &wsRemotePart{gen: gen, genOK: genOK, snap: h.remoteSnapshot()}
	encoded := p.snap.SessionsJSON() // encoded once per snapshot, shared with the HTTP list
	for i, s := range p.snap.Sessions {
		srv, _ := s["server"].(string)
		k := background.FeedKey(s)
		if k == "" {
			continue
		}
		b := encoded[i]
		p.sessions = append(p.sessions, s)
		p.keys = append(p.keys, remoteKey(srv, k))
		p.serialized = append(p.serialized, string(b))
	}
	if p.snap.Configured() {
		b, _ := json.Marshal(p.snap.Servers)
		p.serversJSON = string(b)
	}
	p.builtAt = time.Now()
	return p
}

func assembleWSTick(local *wsLocalPart, remote *wsRemotePart) *wsTick {
	n := len(local.sessions) + len(remote.sessions)
	t := &wsTick{
		sessions:    make([]map[string]any, 0, n),
		current:     make(map[string]string, n),
		byKey:       make(map[string]map[string]any, n),
		hasRemotes:  remote.snap.Configured(),
		activeRuns:  local.activeRuns,
		runsJSON:    local.runsJSON,
		serversJSON: remote.serversJSON,
	}
	if t.hasRemotes {
		t.servers = remote.snap.Servers
	}
	for i, s := range local.sessions {
		t.sessions = append(t.sessions, s)
		if key := local.keys[i]; key != "" {
			t.current[key] = local.serialized[i]
			t.byKey[key] = s
		}
	}
	for i, s := range remote.sessions {
		t.sessions = append(t.sessions, s)
		t.current[remote.keys[i]] = remote.serialized[i]
		t.byKey[remote.keys[i]] = s
	}
	return t
}

// sameJSONValue reports whether a and b are the same value to json.Marshal: the same types holding
// the same data. It knows the types a session row holds (strings, string pointers, bools, integers, floats compared
// bit for bit so 0 and -0 differ, nil, maps, slices and raw JSON); anything else is "not the same",
// which only costs a Marshal. It allocates nothing.
func sameJSONValue(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int:
		y, ok := b.(int)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case *string: // a sleeping row's icon and name color
		y, ok := b.(*string)
		return ok && (x == nil) == (y == nil) && (x == nil || *x == *y)
	case json.RawMessage:
		y, ok := b.(json.RawMessage)
		return ok && bytes.Equal(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameJSONValue(v, w) {
				return false
			}
		}
		return true
	case map[string]string:
		y, ok := b.(map[string]string)
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || v != w {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for i := range x {
			if !sameJSONValue(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return false
}
