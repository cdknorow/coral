package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// An agy agent that waits on a tool permission prompt is waiting for the user, and the live list, the health check, the sidebar badge and the
// Inbox read that from its events (#4819). agy sends no hook for the prompt (Claude's PermissionRequest has no equivalent), only PreToolUse
// before every tool, and most tools run at once: so the hook alone can't say a tool needs approval. A question (ask_question) always waits, and
// the hook records it itself (coral-hook-agentic-state). For the rest this watches the tool Coral was told started: if it is still the pending
// tool after a moment, and the terminal shows a prompt with options, Coral records the request as the permission_request event Claude's hook
// sends, with the tool's name, so the state derivation reads it and the tool's PostToolUse, a stop or a prompt clears it, as for Claude. A tool
// that is only slow shows no prompt and records nothing.

// promptWatchDelays are when the watcher looks, counted from the pending tool (a var for the tests): every 500 ms for 6 s. A tool that
// finishes ends its watcher at the first look (one capture), so the cost is paid only by a tool that is slow or waits on a prompt; the looks
// were 1.5, 3 and 5 s, and agy's permission prompts were recorded at the last of them, 4.2 to 4.5 s from the send (#4874).
var promptWatchDelays = promptLooks(500*time.Millisecond, 6*time.Second)

// promptLooks are the times every, 2*every, ... up to total.
func promptLooks(every, total time.Duration) []time.Duration {
	var out []time.Duration
	for d := every; d <= total; d += every {
		out = append(out, d)
	}
	return out
}

// promptWatchCoarse are the looks of a watcher that starts while promptWatchFine others are running: never a busy loop, however many tools are
// pending at once (a team of agents each running a tool), those look at the old pace.
var promptWatchCoarse = []time.Duration{1500 * time.Millisecond, 3 * time.Second, 5 * time.Second}

// promptWatchFine is how many watchers look at the fine pace at once (a var for the tests).
var promptWatchFine int32 = 32

// promptLooksFor are the looks of a watcher that starts when active watchers (itself included) are running: the fine pace up to
// promptWatchFine of them, the coarse pace past that.
func promptLooksFor(active int32) []time.Duration {
	if active > promptWatchFine {
		return promptWatchCoarse
	}
	return promptWatchDelays
}

var promptWatchActive atomic.Int32

// watchesPrompts: the agents that report no hook of their own for a permission prompt but do send PreToolUse. Antigravity (agy) only: Gemini's
// PreToolUse and prompt screen are not verified.
func watchesPrompts(agentType string) bool {
	switch agentType {
	case at.Agy, at.Antigravity:
		return true
	}
	return false
}

// agyRequestKinds is what a tool's wait asks: agy's ask_question is a question, any other tool's a permission.
var agyRequestKinds = map[string]string{"ask_question": "question"}

// promptWatches are the tools being watched (session id and tool call id), so one tool starts one watcher. A watcher removes its key when it
// ends (the tool finished, a request was recorded, or its looks are over), so the set holds only the tools in flight and never fills up.
var promptWatches sync.Map

// watchForPrompt starts the watcher of a pending tool of an agent that reports no permission hook.
func (h *SessionsHandler) watchForPrompt(sessionID, sessionName string, t pendingTool) {
	if h.ss == nil || h.ts == nil || h.terminal == nil || sessionID == "" || t.ToolUseID == "" {
		return
	}
	ls, err := h.ss.GetLiveSession(context.Background(), sessionID)
	if err != nil || ls == nil || !watchesPrompts(ls.AgentType) {
		return
	}
	key := sessionID + "|" + t.ToolUseID
	if _, running := promptWatches.LoadOrStore(key, struct{}{}); running {
		return
	}
	name, agentType, agentName := sessionName, ls.AgentType, ls.AgentName
	delays := promptLooksFor(promptWatchActive.Add(1))
	safego.Go("server/routes/agy_prompt_watch", func() {
		defer promptWatches.Delete(key)
		defer promptWatchActive.Add(-1)
		var waited time.Duration
		for i, delay := range delays {
			time.Sleep(delay - waited)
			waited = delay
			if h.confirmPromptWait(sessionID, name, agentType, agentName, t, i+1) {
				return
			}
		}
	})
}

// confirmPromptWait looks once. It reports true when there is nothing more to do: the tool finished or the turn moved on, a request is
// already open, or the request was recorded now.
func (h *SessionsHandler) confirmPromptWait(sessionID, name, agentType, agentName string, t pendingTool, look int) bool {
	if cur, ok := h.pending.get(sessionID, time.Now()); !ok || cur.ToolUseID != t.ToolUseID {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), promptReadTimeout)
	defer cancel()
	screen, shown, err := h.readPromptScreen(ctx, name, agentType, sessionID)
	if err != nil || !shown || len(screen.Options) == 0 {
		if debugEnabled() { // why a look missed: what the screen parsed to, for a prompt that is drawn but not read (#4874)
			slog.Info("[debug] agy prompt look found no prompt", "session", name, "tool", t.ToolName, "look", look, "error", err, "prompt_shown", shown, "options", len(screen.Options))
		}
		return false // no prompt on the screen (yet): the tool is only slow, or the terminal could not be read
	}
	byID, err := h.ts.GetSessionStateEvents(ctx, []string{sessionID})
	if err != nil {
		return false
	}
	in := SessionStateInput{}
	for _, e := range byID[sessionID] {
		in.Events = append(in.Events, e.StateEvent())
	}
	if sessionstate.Derive(in).NeedsInput {
		return true // a request is open already (the hook recorded the question): never a second one for the same tool
	}
	kind := "permission"
	if k := agyRequestKinds[t.ToolName]; k != "" {
		kind = k
	}
	detail, _ := json.Marshal(map[string]any{"kind": kind, "tool_name": t.ToolName})
	d := string(detail)
	sid, tool, ref := sessionID, t.ToolName, t.ToolUseID
	if _, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{
		AgentName: agentName, SessionID: &sid, EventType: "permission_request", ToolName: &tool, RefID: &ref,
		Summary: "Waiting for the user: " + kind + " (" + truncateUTF8(t.ToolName, 60) + ")", DetailJSON: &d,
	}); err != nil {
		slog.Warn("could not record an agent's permission wait", "session", name, "error", err)
		return false
	}
	h.notePendingToolEvent(sessionID, "permission_request", t.ToolUseID)
	h.autoAccept.Kick() // an auto-accept session's prompt is answered now, not on the next sweep
	return true
}
