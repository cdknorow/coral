package background

import (
	"context"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
)

// screenReader is what an agent runtime offers to show an agent's screen (the PTY and tmux runtimes both do).
type screenReader interface {
	AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error)
}

// screenLimit reads a usage limit off agy's screen (#5229): its CLI reports the quota being used up only in its own text, with no hook
// event, so an agent that cannot work looked idle to the check. Other agent types report their limits through their transcripts and
// are not looked at here. A runtime that cannot show a screen sees no limit.
func screenLimit(rt AgentRuntime) func(ctx context.Context, a AgentInfo) (time.Duration, bool) {
	sr, ok := rt.(screenReader)
	if !ok {
		return nil
	}
	return func(ctx context.Context, a AgentInfo) (time.Duration, bool) {
		if a.AgentType != at.Agy && a.AgentType != at.Antigravity {
			return 0, false
		}
		screen, err := sr.AgentScreen(ctx, naming.SessionName(a.AgentType, a.SessionID), a.AgentType, a.SessionID)
		if err != nil {
			return 0, false
		}
		return ptymanager.AgyQuotaOnScreen(screen)
	}
}

// blockedByLimit says whether a is stopped by a usage limit, and when it resets.
func (m *BoardHealthMonitor) blockedByLimit(ctx context.Context, a AgentInfo) (time.Duration, bool) {
	if m.limit == nil {
		return 0, false
	}
	return m.limit(ctx, a)
}
