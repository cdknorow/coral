package ptymanager

import (
	"context"
	"log/slog"
	"time"
)

// Coral's own recent texts per session (#247). lastPasted knows only the text of the paste in flight, and forgets it once its
// Enter is seen to empty the box. Codex keeps text that arrives mid-turn and can show it in its input box again later (its
// queued "↳" message back in the composer), and several of Coral's pastes can pile up there while the box can't be read. The
// box then held text that was all Coral's, the gate took it for a person's draft, and every send behind it was held for two
// hours, then dropped. The gate now remembers what it typed and recognises it, alone or several in a row, as its own.
const (
	ownTextsPerSession = 8
	ownTextMaxAge      = 6 * time.Hour
)

type ownText struct {
	text string // squeezed
	at   time.Time
}

// noteOwnText records that the gate is typing text into name.
func (g *inputGate) noteOwnText(name, text string) {
	t := squeeze(text)
	if t == "" {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ownTexts == nil {
		g.ownTexts = map[string][]ownText{}
	}
	list := append(g.ownTexts[name], ownText{text: t, at: now})
	if len(list) > ownTextsPerSession {
		list = list[len(list)-ownTextsPerSession:]
	}
	g.ownTexts[name] = list
}

// ownLeftover reports whether box, the text in name's input box, is all Coral's: the text it last pasted, or one or more of its
// recent texts in the order it typed them, and nothing else (a person's edit makes it a draft).
func (g *inputGate) ownLeftover(name, box string) bool {
	b := squeeze(box)
	if b == "" {
		return false
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastPasted[name] == b {
		return true
	}
	var recent []string
	for _, o := range g.ownTexts[name] {
		if now.Sub(o.at) <= ownTextMaxAge {
			recent = append(recent, o.text)
		}
	}
	for i := range recent {
		run := ""
		for j := i; j < len(recent); j++ {
			run += recent[j]
			if run == b {
				return true
			}
			if len(run) >= len(b) {
				break
			}
		}
	}
	return false
}

// forgetOwnTexts drops name's recorded texts once a leftover of them was submitted, so the same words typed later by a person
// are never taken for Coral's.
func (g *inputGate) forgetOwnTexts(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.ownTexts, name)
}

// Repainter is implemented by terminals that can make the agent redraw its whole screen (the PTY backend's: its rendered screen
// is a replay that can lose the agent's terminal state, #247).
type Repainter interface {
	RepaintSession(ctx context.Context, name, agentType, sessionID string) error
}

// repaintEvery bounds how often the gate asks one session to redraw.
const repaintEvery = 30 * time.Second

// repaintIfDue asks in's terminal for a redraw when its input box can't be found, at most once per repaintEvery for the session.
// The held send's next try reads the redrawn screen.
func (g *inputGate) repaintIfDue(ctx context.Context, in AutomatedInput, agentType, sessionID string) {
	r, ok := in.Term.(Repainter)
	if !ok {
		return
	}
	now := g.now()
	g.mu.Lock()
	if g.repaintedAt == nil {
		g.repaintedAt = map[string]time.Time{}
	}
	if last, seen := g.repaintedAt[in.Name]; seen && now.Sub(last) < repaintEvery {
		g.mu.Unlock()
		return
	}
	g.repaintedAt[in.Name] = now
	g.mu.Unlock()
	if err := r.RepaintSession(ctx, in.Name, agentType, sessionID); err != nil {
		slog.Debug("automated input: asking the agent to redraw failed", "session", in.Name, "error", err)
		return
	}
	slog.Info("automated input: the input box isn't on the rendered screen; asked the agent to redraw", "session", in.Name, "sender", in.What)
}
