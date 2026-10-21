package routes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// #3655 F2: Claude's question and plan menus block the gate from their PreToolUse on, before
// any notification, and in the hook set agents launched before v1.4.27 still run (no
// PermissionRequest). The menu's PostToolUse, or an end of turn after it (Esc fires no hook),
// closes it; another tool's PreToolUse is no menu.
func TestGateStateBlocksOnAnOpenQuestionOrPlanMenu(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir()) // no transcript
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000003655"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "folder-3655", AgentType: "claude", WorkingDir: t.TempDir()}))
	h.hooksRev.Store(sid, agent.ClaudeHooksRev) // a current agent's hooks
	id := sid
	event := func(typ, tool string) {
		_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "folder-3655", SessionID: &id, EventType: typ, ToolName: &tool, Summary: typ})
		require.NoError(t, err)
	}
	state := func() ptymanager.AgentState {
		st, ok := h.gateState(ctx, "claude-"+sid, "claude", sid)
		require.True(t, ok)
		return st
	}

	event("tool_use", "Bash") // the turn runs (an old hook set has no prompt_submit)
	assert.Equal(t, ptymanager.StateInTurn, state().State)

	opened := time.Now()
	h.pending.set(sid, pendingTool{ToolUseID: "tu-q", ToolName: "AskUserQuestion", At: opened})
	st := state()
	assert.Equal(t, ptymanager.StateBlocked, st.State, "the question menu is open: blocked before any notification")
	assert.Equal(t, "question", st.Kind)
	assert.True(t, st.Since.Equal(opened), "blocked since the menu opened")

	h.notePendingToolEvent(sid, "tool_use", "tu-q") // its PostToolUse: answered
	event("tool_use", "AskUserQuestion")
	assert.Equal(t, ptymanager.StateInTurn, state().State, "answered: back in the turn")

	h.pending.set(sid, pendingTool{ToolUseID: "tu-p", ToolName: "ExitPlanMode", At: time.Now()})
	st = state()
	assert.Equal(t, ptymanager.StateBlocked, st.State)
	assert.Equal(t, "plan", st.Kind, "a plan approval")

	h.pending.set(sid, pendingTool{ToolUseID: "tu-b", ToolName: "Bash", At: time.Now()})
	assert.Equal(t, ptymanager.StateInTurn, state().State, "another tool's PreToolUse is no menu")

	// Esc closes a menu with no hook: the menu stays pending, but the turn ended after it.
	h.pending.set(sid, pendingTool{ToolUseID: "tu-esc", ToolName: "AskUserQuestion", At: time.Now().Add(-time.Minute)})
	event("stop", "")
	assert.Equal(t, ptymanager.StateIdle, state().State, "an end of turn after the menu: closed")

	// QA's point 2: only an end of turn strictly AFTER the menu closes it. That stop is now stale:
	// a menu opened after it (the next turn's) is open.
	time.Sleep(5 * time.Millisecond)
	h.pending.set(sid, pendingTool{ToolUseID: "tu-new", ToolName: "AskUserQuestion", At: time.Now()})
	st = state()
	assert.Equal(t, ptymanager.StateBlocked, st.State, "a stale end of turn doesn't close a newer menu")
	assert.Equal(t, "question", st.Kind)

	// Not for other CLIs: their prompts have their own signals (#3497).
	st, ok := h.gateState(ctx, "codex-"+sid, "codex", sid)
	require.True(t, ok)
	assert.NotEqual(t, ptymanager.StateBlocked, st.State)
}
