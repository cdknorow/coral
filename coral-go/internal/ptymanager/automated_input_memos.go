// What the gate remembers per session: ended sessions, delivered keys, and the held-send failure log (moved out of automated_input.go, #5091; no behaviour change).

package ptymanager

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/logdedupe"
	"github.com/subgentic/coral/internal/tmux"
)

// sessionOfTarget is the session a tmux target names: "claude-x", "claude-x.0",
// "claude-x:0" and "claude-x:0.0" are all claude-x (QA's #3431 v3 live check:
// the restart and launch paths pass pane targets).
func sessionOfTarget(target string) string {
	if i := strings.IndexByte(target, ':'); i >= 0 {
		target = target[:i]
	}
	return strings.TrimSuffix(target, ".0")
}

// sessionEndHooks clear what other packages remember per session (a log line already
// written for it, and the like) when DropHeld ends that session's state, so those memos
// don't grow with every session the server has ever run (#4057).
var (
	sessionEndMu    sync.Mutex
	sessionEndHooks []func(name string)
)

// OnSessionEnd registers f to be called with a session's terminal name ("<type>-<id>")
// whenever its agent is killed, restarted, resumed or launched again (DropHeld). Register
// once, at package init: hooks are never removed.
func OnSessionEnd(f func(name string)) {
	sessionEndMu.Lock()
	defer sessionEndMu.Unlock()
	sessionEndHooks = append(sessionEndHooks, f)
}

func forgetSessionMemos(name string) {
	tmux.ForgetBareNameWarning(name)
	sessionEndMu.Lock()
	hooks := append([]func(string){}, sessionEndHooks...)
	sessionEndMu.Unlock()
	for _, f := range hooks {
		f(name)
	}
}

// HeldCount is the number of sends held for a session, the one the retry loop is trying now included (the sessions list, tests and diagnostics).
func HeldCount(name string) int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	n := len(gate.held[name])
	if gate.inflight[name] != nil {
		n++
	}
	return n
}

// HeldSince is when the oldest automated send held for a session was first held (the one the retry loop is trying now included), the zero time when none is: the live list's held_since, so a
// page that opens late can say how long a send has waited (#6503). It starts again when the count returns to zero.
func HeldSince(name string) time.Time {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	var oldest time.Time
	for _, h := range gate.held[name] {
		if oldest.IsZero() || h.heldAt.Before(oldest) {
			oldest = h.heldAt
		}
	}
	if in := gate.inflight[name]; in != nil && (oldest.IsZero() || in.heldAt.Before(oldest)) {
		oldest = in.heldAt
	}
	return oldest
}

// deliveredKeysPerSession bounds LastDelivered's record for one session.
const deliveredKeysPerSession = 64

// noteDelivered records that a send with key was just typed into name (LastDelivered).
func (g *inputGate) noteDelivered(name, key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.dialogHeld, name) // delivered: no dialog in the way any more (#4104)
	m := g.delivered[name]
	if m == nil {
		m = map[string]time.Time{}
		g.delivered[name] = m
	}
	if _, ok := m[key]; !ok && len(m) >= deliveredKeysPerSession {
		oldest, at := "", time.Time{}
		for k, t := range m {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(m, oldest)
	}
	m[key] = g.now()
}

// LastDelivered returns when a send with key was last typed into the session name, or
// the zero time if never: recorded when the gate actually writes it (at once, or on a
// held send's turn), never for a send that is held, handed off (TakeHeld) or dropped;
// DropHeld (the session ended or restarted) forgets it. The turn-end hand-off uses it
// to stand down when a nudge was typed during the turn, so it is already queued in the
// agent's input (#3510).
func LastDelivered(name, key string) time.Time {
	name = sessionOfTarget(name)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.delivered[name][key]
}

// heldSendFailures coalesces the held-send failure warning (#4112 part 2, Security's #4107 audit:
// 100 failed sends to a pane that was gone gave a warning each): the same session, sender and
// error log once, then once every 5 minutes with how many were held back.
var heldSendFailures = logdedupe.New(5 * time.Minute)

func logHeldSendFailure(name, what string, err error) {
	ok, heldBack := heldSendFailures.Allow(name + "\x00" + what + "\x00" + err.Error())
	if !ok {
		return
	}
	if heldBack > 0 {
		slog.Warn("automated input: a held send failed", "session", name, "sender", what, "error", err, "more_like_it", heldBack, "in_the_last", "5m")
		return
	}
	slog.Warn("automated input: a held send failed", "session", name, "sender", what, "error", err)
}
