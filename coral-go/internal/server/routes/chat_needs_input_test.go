package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3720: the chat poll's reply carries the pending tool (always) and the options of an open prompt (only when
// one may be open), so the dashboard needs no separate poll for them.

func chatReply(t *testing.T, base string, q url.Values) map[string]any {
	t.Helper()
	resp, err := http.Get(base + "/chat?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func TestChatReplyCarriesThePendingTool(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4a7"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-need", WorkingDir: "/tmp/test"}))
	base := server.URL + "/api/sessions/live/claude-need"
	q := url.Values{"session_id": {sid}, "agent_type": {"claude"}, "after": {"3"}}

	r := chatReply(t, base, q)
	v, has := r["pending"]
	assert.True(t, has, "the key is there even when nothing is pending")
	assert.Nil(t, v)

	handler.pending.set(sid, pendingTool{ToolUseID: "toolu_1", ToolName: "AskUserQuestion", Input: map[string]any{"q": "which?"}, At: time.Now()})
	r = chatReply(t, base, q)
	p, ok := r["pending"].(map[string]any)
	require.True(t, ok, "a started tool is in the reply: %v", r["pending"])
	assert.Equal(t, "AskUserQuestion", p["tool_name"])

	// The first page (after=0) carries it too.
	first := chatReply(t, base, url.Values{"session_id": {sid}, "agent_type": {"claude"}, "limit": {"10"}})
	_, has = first["pending"]
	assert.True(t, has, "the first page has the pending key as well")

	// A poll by name alone is resolved to its session from the live record, so it gets the key too.
	byName := chatReply(t, base, url.Values{"after": {"3"}})
	_, has = byName["pending"]
	assert.True(t, has, "a name the server can resolve to a session")
	// A name that is no session at all has nothing to look up: no pending key.
	unknown := chatReply(t, server.URL+"/api/sessions/live/claude-nobody", url.Values{"after": {"3"}})
	_, has = unknown["pending"]
	assert.False(t, has)
}

func TestChatReplyReadsPromptOptionsOnlyWhenAPromptMayBeOpen(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4a8"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-need2", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-need2", "/tmp/test")
	terminal.mu.Lock()
	terminal.outputs["claude-need2"] = screenPermission
	terminal.mu.Unlock()
	base := server.URL + "/api/sessions/live/claude-need2"
	q := url.Values{"session_id": {sid}, "agent_type": {"claude"}, "after": {"3"}}
	captures := func() int { terminal.mu.Lock(); defer terminal.mu.Unlock(); return len(terminal.captures) }
	options := func(r map[string]any) int {
		po, ok := r["prompt_options"].(map[string]any)
		if !ok {
			return -1
		}
		o, _ := po["options"].([]any)
		return len(o)
	}

	// No hook event seen yet: unknown means may be open, so the screen is read, as the old route did.
	r := chatReply(t, base, q)
	assert.Equal(t, 4, options(r), "unknown: the options are read")

	// The agent is working (a turn started, the hook says so): no prompt is open and nothing is read.
	handler.hints.note(sid, "prompt_submit")
	before := captures()
	r = chatReply(t, base, q)
	_, has := r["prompt_options"]
	assert.False(t, has, "a working agent: no prompt_options in the reply")
	assert.Equal(t, before, captures(), "and no pane capture was made for it")

	// An event that does not open or close a request does not change that.
	handler.hints.note(sid, "goal")
	_, has = chatReply(t, base, q)["prompt_options"]
	assert.False(t, has)

	// A permission request or a notification: a prompt may be open, so the options come with the reply.
	handler.hints.note(sid, "permission_request")
	assert.Equal(t, 4, options(chatReply(t, base, q)))
	handler.hints.note(sid, "request_resolved")
	_, has = chatReply(t, base, q)["prompt_options"]
	assert.False(t, has, "resolved: not read again")
	handler.hints.note(sid, "notification")
	assert.Equal(t, 4, options(chatReply(t, base, q)))
	handler.hints.note(sid, "stop")
	_, has = chatReply(t, base, q)["prompt_options"]
	assert.False(t, has)

	// A pending blocking tool (AskUserQuestion) opens the read whatever the hints say.
	handler.pending.set(sid, pendingTool{ToolUseID: "toolu_2", ToolName: "AskUserQuestion", At: time.Now()})
	assert.Equal(t, 4, options(chatReply(t, base, q)), "a pending tool: read")
}

func TestChatReplyAlwaysReadsTheScreenOfAgentsWithoutPromptHooks(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4a9"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "codex-need", WorkingDir: "/tmp/test"}))
	terminal.addSession("codex-need", "/tmp/test")
	terminal.mu.Lock()
	terminal.outputs["codex-need"] = screenPermission
	terminal.mu.Unlock()
	handler.hints.note(sid, "stop")
	r := chatReply(t, server.URL+"/api/sessions/live/codex-need", url.Values{"session_id": {sid}, "agent_type": {"codex"}, "after": {"1"}})
	po, ok := r["prompt_options"].(map[string]any)
	require.True(t, ok, "Codex has no hook for its prompts: its screen is read on every poll")
	assert.NotEmpty(t, po["options"])
}

func TestChatReplyNeverReadsAnotherAgentsPane(t *testing.T) {
	// The exact-target check GET /prompt-options used to make: a session id that does not belong to the named
	// terminal gets no screen from it.
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4aa"
	const other = "00000000-0000-0000-0000-00000000c4ab"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-mine", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-mine", "/tmp/test")
	terminal.mu.Lock()
	terminal.outputs["claude-mine"] = screenPermission
	terminal.mu.Unlock()
	r := chatReply(t, server.URL+"/api/sessions/live/claude-mine", url.Values{"session_id": {other}, "agent_type": {"claude"}, "after": {"1"}})
	_, has := r["prompt_options"]
	assert.False(t, has, "an id that is not this terminal's session: its pane is not read")
}

func TestPromptHintsAreBounded(t *testing.T) {
	var p promptHints
	for i := 0; i < promptHintsMax+10; i++ {
		p.note("s"+string(rune('a'+i%26))+time.Duration(i).String(), "stop")
	}
	p.mu.Lock()
	n := len(p.m)
	p.mu.Unlock()
	assert.LessOrEqual(t, n, promptHintsMax, "the table is bounded; forgetting a session only turns it back to unknown")
	assert.True(t, p.mayBeOpen("never-seen"), "unknown means may be open")
	p.note("gone", "permission_request")
	assert.True(t, p.mayBeOpen("gone"))
	p.note("gone", "session_end")
	assert.True(t, p.mayBeOpen("gone"), "an ended session is forgotten, so unknown again")
}

// #3745: one read of a session's prompt options answers its polls for promptReadTTL. A session whose hook
// state is unknown (or with no prompt hooks) was read on every poll, a pane capture each.
func TestChatReplyReadsThePaneOncePerWindow(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4b1"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-win", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-win", "/tmp/test")
	terminal.mu.Lock()
	terminal.outputs["claude-win"] = screenPermission
	terminal.mu.Unlock()
	keep := promptReadTTL
	promptReadTTL = 300 * time.Millisecond
	t.Cleanup(func() { promptReadTTL = keep })
	base := server.URL + "/api/sessions/live/claude-win"
	q := url.Values{"session_id": {sid}, "agent_type": {"claude"}, "after": {"1"}}
	captures := func() int { terminal.mu.Lock(); defer terminal.mu.Unlock(); return len(terminal.captures) }
	options := func(r map[string]any) int {
		po, ok := r["prompt_options"].(map[string]any)
		if !ok {
			return -1
		}
		o, _ := po["options"].([]any)
		return len(o)
	}

	before := captures()
	for i := 0; i < 5; i++ {
		assert.Equal(t, 4, options(chatReply(t, base, q)), "poll %d has the options", i)
	}
	assert.Equal(t, before+1, captures(), "five polls inside the window: one capture")

	// Concurrent polls in a new window share one read.
	time.Sleep(promptReadTTL + 50*time.Millisecond) // sleep-ok: the window has to run out
	before = captures()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); assert.Equal(t, 4, options(chatReply(t, base, q))) }()
	}
	wg.Wait()
	assert.Equal(t, before+1, captures(), "eight polls at once: one capture")

	// A hook event drops the read: the next poll reads the screen again, inside the window.
	before = captures()
	handler.hints.note(sid, "notification")
	assert.Equal(t, 4, options(chatReply(t, base, q)))
	assert.Equal(t, before+1, captures(), "a hook event: read again at once")
	// So do a pending tool and an answer (forget), the paths SetPendingTool and AnswerPrompt take.
	before = captures()
	handler.hints.forget(sid)
	chatReply(t, base, q)
	assert.Equal(t, before+1, captures(), "forgotten: read again")
}

// A read is cached for its exact target only: a poll that names another terminal for the same session id is
// checked on its own and gets no screen.
func TestChatReplyCachedReadIsTheExactTargets(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000c4b2"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-own", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-own", "/tmp/test")
	terminal.addSession("claude-else", "/tmp/test")
	terminal.mu.Lock()
	terminal.outputs["claude-own"] = screenPermission
	terminal.mu.Unlock()
	q := url.Values{"session_id": {sid}, "agent_type": {"claude"}, "after": {"1"}}
	r := chatReply(t, server.URL+"/api/sessions/live/claude-own", q)
	_, has := r["prompt_options"]
	require.True(t, has, "its own terminal: read and cached")
	r = chatReply(t, server.URL+"/api/sessions/live/claude-else", q)
	_, has = r["prompt_options"]
	assert.False(t, has, "another terminal with this session's id: not given the cached screen")
}
