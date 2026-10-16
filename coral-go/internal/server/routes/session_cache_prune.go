package routes

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/logparse"
)

// sessionCachePruneEvery is how often per-session caches are swept of sessions
// that are no longer live. A session that ends keeps its cached transcript
// (every parsed message) at most this long.
const sessionCachePruneEvery = time.Minute

// sessionCachePruneState throttles pruneSessionCaches; zero value is ready.
type sessionCachePruneState struct {
	last    atomic.Int64 // unix nanos of the last sweep start
	running atomic.Bool
}

// maybePruneSessionCaches starts a background sweep when the last one is older
// than sessionCachePruneEvery. It never blocks the caller.
func (h *SessionsHandler) maybePruneSessionCaches() {
	p := &h.cachePrune
	now := time.Now().UnixNano()
	if now-p.last.Load() < int64(sessionCachePruneEvery) || !p.running.CompareAndSwap(false, true) {
		return
	}
	p.last.Store(now)
	go func() {
		defer p.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h.pruneSessionCaches(ctx)
	}()
}

// pruneSessionCaches drops per-session cache entries (parsed transcripts,
// first prompts, transcript warm-up marks) for sessions that are neither
// registered as live (sleeping included), the ancestor a live one resumed from, nor running in
// the terminal backend.
// If either source cannot be read, nothing is dropped.
func (h *SessionsHandler) pruneSessionCaches(ctx context.Context) int {
	live, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		return 0
	}
	keep := make(map[string]bool, len(live))
	for _, s := range live {
		keep[s.SessionID] = true
		// A resumed session's chat reads its ancestor's transcript too (the lineage, Chat):
		// dropping the ancestor made the next poll re-parse it whole, every minute (#3710).
		if a := derefStrPtr(s.ResumeFromID); a != "" {
			keep[a] = true
		}
	}
	if h.terminal != nil {
		panes, err := h.terminal.ListSessions(ctx)
		if err != nil {
			return 0
		}
		for _, p := range panes {
			if _, sid := logparse.ParseSessionName(p.SessionName); sid != "" {
				keep[sid] = true
			}
		}
	}
	isLive := func(id string) bool { return keep[id] }

	dropped := 0
	if h.jsonl != nil {
		dropped = h.jsonl.Retain(isLive)
	}
	h.transcriptWarmMu.Lock()
	for id := range h.transcriptWarm {
		if !isLive(id) {
			delete(h.transcriptWarm, id)
			dropped++
		}
	}
	h.transcriptWarmMu.Unlock()
	if dropped > 0 {
		slog.Debug("pruned caches of ended sessions", "entries", dropped, "live", len(keep))
	}
	return dropped
}
