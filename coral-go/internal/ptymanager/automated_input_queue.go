// The gate's held queue: holding, flushing, giving up behind a dialog, and the loop that expires and reports (moved out of automated_input.go, #5091; no behaviour change).

package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/safego"
)

func (g *inputGate) lockSession(name string) func() {
	m, _ := g.sending.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (g *inputGate) hold(in AutomatedInput, why, kind string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.staleLocked(in) {
		return
	}
	q := g.held[in.Name]
	for i, h := range q {
		if h.in.Key == in.Key {
			// Superseded: the newer send replaces it, in its place.
			q[i] = &heldInput{in: in, heldAt: h.heldAt, why: why, kind: kind, reported: h.reported, reportedPrompt: h.reportedPrompt}
			g.held[in.Name] = q
			g.noteDialogLocked(in.Name, kind)
			g.logHeld(in, why, "replaced a held send")
			return
		}
	}
	if len(q) >= heldPerSession {
		slog.Warn("automated input: too many held sends; dropping the oldest", "session", in.Name, "dropped", q[0].in.What)
		q = q[1:]
	}
	// And at most heldBytesPerSession of text (#3578: in signals mode a long turn holds every
	// routine send for it). Never the new one; the log names the sender, never the text.
	total := len(in.Text)
	for _, h := range q {
		total += len(h.in.Text)
	}
	for len(q) > 0 && total > heldBytesPerSession {
		slog.Warn("automated input: held sends over the size limit; dropping the oldest", "session", in.Name, "dropped", q[0].in.What)
		total -= len(q[0].in.Text)
		q = q[1:]
	}
	g.held[in.Name] = append(q, &heldInput{in: in, heldAt: g.now(), why: why, kind: kind})
	g.noteDialogLocked(in.Name, kind)
	g.logHeld(in, why, "held")
	if !g.running {
		g.running = true
		g.startLoop()
	}
}

func (g *inputGate) logHeld(in AutomatedInput, why, what string) {
	slog.Info("automated input "+what, "session", in.Name, "sender", in.What, "why", why)
	inputlog.Note(in.Name, inputlog.Text, "[HELD, "+why+"] "+in.Text)
}

// flush types what is held for name if the agent is no longer waiting. It
// reports whether something is still held (the agent is waiting again).
func (g *inputGate) flush(ctx context.Context, name string) bool {
	still := g.flushQueue(ctx, name)
	g.checkEnded(name)
	return still
}

// staleLocked reports (and logs) a send that reached the gate before DropHeld last
// ran for its session: it belongs to the old process and is dropped, not held.
func (g *inputGate) staleLocked(in AutomatedInput) bool {
	if in.gen == g.gens[in.Name] {
		return false
	}
	slog.Info("automated input: dropped a send for an agent that was restarted or killed", "session", in.Name, "sender", in.What)
	return true
}

// checkEnded calls HeldEndedFunc when name had a reported send and has none held now.
func (g *inputGate) checkEnded(name string) {
	g.mu.Lock()
	if g.dialogHeld[name] {
		// The dialog's notice stays until a send is delivered again (#4104): a mention given up
		// behind it empties the queue, but the dialog is still open.
		g.mu.Unlock()
		return
	}
	in, ok := g.reportedFor[name]
	if ok {
		for _, h := range g.held[name] {
			if h.reported {
				g.mu.Unlock()
				return
			}
		}
		delete(g.reportedFor, name)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	heldEnded.RLock()
	fn := heldEnded.fn
	heldEnded.RUnlock()
	if fn != nil {
		g.notes.post(func() { fn(in.Name, in.AgentType, in.SessionID) })
	}
}

func (g *inputGate) flushQueue(ctx context.Context, name string) bool {
	for {
		g.mu.Lock()
		q := g.held[name]
		if len(q) == 0 {
			delete(g.held, name)
			g.mu.Unlock()
			return false
		}
		h := q[0]
		g.mu.Unlock()

		// Checked again just before each send: the prompt may come back.
		in := h.in
		g.mu.Lock()
		if in.heldSince.IsZero() {
			in.heldSince = h.heldAt // waiting's in-turn fallback counts from it when the turn's start is unknown
		}
		if in.RequireInputBox && g.now().Sub(h.heldAt) > g.timings.tooLong {
			in.RequireInputBox = false
		}
		g.mu.Unlock()
		if waiting, why, kind := in.waiting(ctx); waiting {
			if kind == KindDialog && g.dismissSurvey(ctx, in) {
				continue // the CLI's session survey was the dialog and is gone: look again (#5114, agy #5229)
			}
			if kind == KindDialog && g.giveUpMentionsBehindDialog(name) {
				continue // the head was a mention given up: look at the next
			}
			// Keep the current reason: a send held for a busy footer or a draft
			// that a real prompt now holds must say so (QA, after #3478).
			g.mu.Lock()
			changed := h.why != why
			h.why, h.kind = why, kind
			g.noteDialogLocked(name, kind)
			g.mu.Unlock()
			if changed {
				g.logHeld(h.in, why, "still held")
			}
			return true
		}
		g.mu.Lock()
		// Remove it before typing, so it is typed once even if Send fails.
		if cur := g.held[name]; len(cur) > 0 && cur[0] == h {
			g.held[name] = cur[1:]
			if g.inflight == nil {
				g.inflight = map[string]*heldInput{}
			}
			g.inflight[name] = h
		} else {
			g.mu.Unlock()
			continue // replaced meanwhile: look again
		}
		send := h.in
		send.heldSince, send.heldWhy, send.heldReported, send.heldReportedPrompt = h.heldAt, h.why, h.reported, h.reportedPrompt
		send.urgent = mentionWaiting(ctx)
		for _, behind := range g.held[name] {
			send.urgent = send.urgent || behind.in.Mention
		}
		g.mu.Unlock()
		enterHeld, err := g.sendChecked(ctx, send)
		g.mu.Lock()
		if g.inflight[name] == h { // not already replaced by the send's own hold
			delete(g.inflight, name)
		}
		g.mu.Unlock()
		if enterHeld {
			return true
		} else if errors.Is(err, ErrTeamBusy) {
			// Not now (#3613): back at the front, for the loop's next pass.
			g.mu.Lock()
			h.why, h.kind = whyTeamSpaced, ""
			g.held[name] = append([]*heldInput{h}, g.held[name]...)
			g.mu.Unlock()
			return true
		} else if errors.Is(err, ErrNotWanted) {
			g.shadowForget(name, h.in.Key)
			slog.Info("automated input dropped after the hold ended: no longer wanted", "session", name, "sender", h.in.What,
				"held_for", g.now().Sub(h.heldAt).Round(time.Second).String())
		} else if err != nil {
			logHeldSendFailure(name, h.in.What, err)
		} else {
			g.shadowForget(name, h.in.Key)
			g.noteDelivered(name, h.in.Key)
			noteDeliveredText(name, h.in.Text)
			slog.Info("automated input delivered after the hold ended", "session", name, "sender", h.in.What, "held_for", g.now().Sub(h.heldAt).Round(time.Second).String(), "released", releasedBecause(h.why))
		}
	}
}

// giveUpMentionsBehindDialog gives up every mention held for name behind a dialog for
// dialogGiveUpAfter (#4104): never pasted, never typed into the dialog, and reported (the note
// says why). It reports whether the queue's head was one of them.
func (g *inputGate) giveUpMentionsBehindDialog(name string) (headGone bool) {
	g.mu.Lock()
	limit := g.dialogGiveUpAfter() // reads g.timings: under the lock, as setTimings writes it (#5266)
	q := g.held[name]
	var gone []AutomatedInput
	kept := q[:0:0]
	for i, h := range q {
		if h.in.Mention && g.now().Sub(h.heldAt) >= limit {
			in := h.in
			in.heldSince = h.heldAt
			gone = append(gone, in)
			if i == 0 {
				headGone = true
			}
			continue
		}
		kept = append(kept, h)
	}
	if len(gone) > 0 {
		g.held[name] = kept
	}
	g.mu.Unlock()
	for _, in := range gone {
		_ = g.giveUpPaste(in, fmt.Sprintf("%s %s", WhyDialogGaveUp, limit.Round(time.Minute)))
	}
	return headGone
}

// startLoop runs the retry loop in its own goroutine. The caller has set running.
func (g *inputGate) startLoop() {
	g.loops.Add(1)
	go func() {
		defer safego.Recover("ptymanager/automated_input:g.loop")
		defer g.loops.Done() // runs first on a panic, so a stop never waits for a loop that has died
		g.loop()
	}()
}

// sleepOrStop waits d, or less when the loop is asked to stop; it reports whether it was.
func (g *inputGate) sleepOrStop(d time.Duration) (stopped bool) {
	g.mu.Lock()
	if g.stopping {
		g.mu.Unlock()
		return true
	}
	if g.stop == nil {
		g.stop = make(chan struct{})
	}
	stop := g.stop
	g.mu.Unlock()
	g.sleeps.Add(1)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return false
	case <-stop:
		return true
	}
}

// StopGate stops the gate's retry loop and waits up to timeout for it to end, reporting whether it
// did. Sends held at that moment stay held (a later hold starts the loop again), so this is for the
// end of Coral and of a test, not a way to cancel input. Coral's shutdown calls it so the loop's
// sleep doesn't outlive the server, and the test mains of the packages that hold input call it
// before the leak check (#4167): a short test run ends before the loop's 2 s sleep does.
func StopGate(timeout time.Duration) bool { return gate.stopLoop(timeout) }

func (g *inputGate) stopLoop(timeout time.Duration) bool {
	g.mu.Lock()
	g.stopping = true
	if g.stop != nil {
		close(g.stop)
		g.stop = nil
	}
	g.mu.Unlock()
	done := make(chan struct{})
	go func() { defer safego.Recover("ptymanager/automated_input:stopLoop"); g.loops.Wait(); close(done) }()
	ok := true
	select {
	case <-done:
	case <-time.After(timeout):
		ok = false
	}
	g.mu.Lock()
	g.stopping = false
	g.mu.Unlock()
	return ok
}

func (g *inputGate) loop() {
	for {
		g.mu.Lock()
		every := g.timings.every
		g.mu.Unlock()
		if g.sleepOrStop(every) {
			g.mu.Lock()
			g.running = false // held sends stay held; the next hold starts a loop again
			g.mu.Unlock()
			return
		}
		g.mu.Lock()
		names := make([]string, 0, len(g.held))
		for n := range g.held {
			names = append(names, n)
		}
		if len(names) == 0 {
			g.running = false
			g.mu.Unlock()
			return
		}
		g.expireAndReportLocked()
		g.mu.Unlock()
		for _, n := range names {
			// One session's panic must not stop the loop, or held input would stay held for every
			// session (running stays true) (#3998).
			func() {
				defer safego.Recover("automated input: flush " + n)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				unlock := g.lockSession(n)
				defer unlock()
				g.flush(ctx, n)
			}()
		}
	}
}

// expireAndReportLocked drops sends held past the expiry and reports, once per
// send, the ones held longer than tooLong.
func (g *inputGate) expireAndReportLocked() {
	now := g.now()
	heldTooLong.RLock()
	report := heldTooLong.fn
	heldTooLong.RUnlock()
	for name, q := range g.held {
		kept := q[:0]
		for _, h := range q {
			if !h.in.RequireInputBox && now.Sub(h.heldAt) > g.timings.expiry {
				slog.Warn("automated input: dropping a send held too long", "session", name, "sender", h.in.What)
				continue
			}
			after := g.timings.tooLong
			if h.kind == KindDialog && g.dialogReportAfter() < after {
				after = g.dialogReportAfter() // something to dismiss now (#4104)
			}
			if (!h.reported || (h.kind != "" && !h.reportedPrompt)) && now.Sub(h.heldAt) > after && report != nil {
				h.reported = true
				h.reportedPrompt = h.reportedPrompt || h.kind != ""
				g.reportedFor[name] = h.in
				in, kind := h.in, h.kind
				g.notes.post(func() { report(in.Name, in.AgentType, in.SessionID, in.What, kind) })
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 {
			delete(g.held, name)
		} else {
			g.held[name] = kept
		}
	}
}

// DropHeld discards everything held for a session: its agent is being killed,
// restarted, resumed or launched again in that terminal, and a send held for
// the old process (a stale nudge, or a first prompt) must never be typed into
// the new one (#3431). The terminals call it from their create, kill, restart
// and launch-command paths.
func DropHeld(name, why string) {
	name = sessionOfTarget(name)
	gate.mu.Lock()
	n := len(gate.held[name])
	delete(gate.held, name)
	delete(gate.inflight, name) // an attempt in flight is abandoned with it (its gen is stale)
	gate.gens[name]++
	gate.shadowForgetSessionLocked(name)
	delete(gate.delivered, name)
	delete(gate.lastPasted, name)
	delete(gate.dialogHeld, name)
	gate.mu.Unlock()
	noteDisagreement(name, "", time.Now()) // its disagreement window, if any, ends with it (#3655)
	gate.checkEnded(name)
	forgetSessionMemos(name)
	callHeldDropped(name, why)
	if n > 0 {
		slog.Info("automated input: dropped held sends", "session", name, "count", n, "why", why)
		inputlog.Note(name, inputlog.Text, "[DROPPED "+strconv.Itoa(n)+" held, "+why+"]")
	}
}
