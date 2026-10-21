package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// #3481 phase 1B (#3475 3A): when a Claude team agent's turn ends with unread board
// messages, its Stop hook is told to keep it going with them, and the nudge Coral held
// for the same messages is retired, never typed. Only the agent's own hook may ask, once
// per stop chain; an agent with no board, another CLI, or the setting off get nothing.
func TestTurnEndHandoff(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3481-1b00-0000-000000000005"
	name := "claude-" + sid
	board := "team-3481"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "echo", WorkingDir: "/w", BoardName: &board}))
	tok := agentToken(t, h, sid, name)
	_, err := h.bs.Subscribe(ctx, board, "Worker", "Worker", name, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, board, "Lead", "Lead", "", nil, "all")
	require.NoError(t, err)

	ask := func(body, tok string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/echo/turn-end", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set(store.SessionTokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, _ := ask(`{}`, "")
	assert.Equal(t, http.StatusUnauthorized, code)
	_, out := ask(`{}`, tok)
	assert.Equal(t, false, out["handoff"], "nothing unread: the turn ends")

	_, err = h.bs.PostMessage(ctx, board, "Lead", "@Worker please review #12", nil)
	require.NoError(t, err)
	// Coral holds the nudge for it (the agent's screen shows a prompt), and an unrelated send.
	for _, key := range []string{"board-unread:" + board, "ui-request:9"} {
		_, err := ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: menuOnScreen{}, Name: name, AgentType: "claude", SessionID: sid,
			Key: key, Text: "held " + key, What: "test", Send: func(context.Context) error { return nil }})
		require.NoError(t, err)
	}
	require.Equal(t, 2, ptymanager.HeldCount(name))

	_, out = ask(`{"stop_hook_active":true}`, tok)
	assert.Equal(t, false, out["handoff"], "at most once per stop chain")

	_, out = ask(`{}`, tok)
	require.Equal(t, true, out["handoff"])
	assert.Equal(t, "New board messages", out["reason"])
	assert.Contains(t, out["context"], "1 new message on "+board)
	assert.Contains(t, out["context"], "coral-board read")
	assert.Equal(t, 1, ptymanager.HeldCount(name), "the board nudge is retired; the other send stays held")
	ptymanager.DropHeld(name, "test done")

	// The setting off, or an agent with no board (a person drives it): no hand-off.
	require.NoError(t, ss.SetSetting(ctx, TurnEndHandoffSetting, "off"))
	_, out = ask(`{}`, tok)
	assert.Equal(t, false, out["handoff"], "turned off")
	require.NoError(t, ss.SetSetting(ctx, TurnEndHandoffSetting, "on"))
	sid2 := "eeeeeeee-3481-1b00-0000-000000000006"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid2, AgentType: "claude", AgentName: "solo", WorkingDir: "/w"}))
	_, out = ask(`{}`, agentToken(t, h, sid2, "claude-"+sid2))
	assert.Equal(t, false, out["handoff"], "no board")
}

// idleBox is an agent at its empty input box: the gate types into it.
type idleBox struct{}

func (idleBox) AgentScreen(context.Context, string, string, string) (string, error) {
	return "⏺ Done.\n\n╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n", nil
}

// #3481 1B with #3482's TakeHeld (taken, ok): when a send to the agent is in progress
// and doesn't finish within the hand-off's wait, nothing is handed off. That send is
// about to reach the agent; a hand-off too would be a second copy.
func TestTurnEndHandoffSkipsWhileASendIsInProgress(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3481-1b00-0000-000000000007"
	name := "claude-" + sid
	board := "team-3481b"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "foxtrot", WorkingDir: "/w", BoardName: &board}))
	tok := agentToken(t, h, sid, name)
	_, err := h.bs.Subscribe(ctx, board, "Worker", "Worker", name, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, board, "Lead", "Lead", "", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.PostMessage(ctx, board, "Lead", "@Worker news", nil)
	require.NoError(t, err)

	// A send that is being typed holds the agent's send lock until it finishes.
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: idleBox{}, Name: name, AgentType: "claude", SessionID: sid,
			Key: "board-unread:" + board, Text: "You have 1 unread message", What: "test",
			Send: func(context.Context) error { close(started); <-release; return nil }})
	}()
	<-started
	defer func() { close(release); <-done }()

	req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/foxtrot/turn-end", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.SessionTokenHeader, tok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, false, out["handoff"], "a send in progress: no second copy through the hand-off")
}

// #3510 (QA's bug bash): with the screen rule (the default), a nudge isn't held during a
// turn; it is typed and Claude queues it. A hand-off at the turn's end would then deliver
// the same messages a second time. So a board nudge typed AFTER the turn's prompt_submit
// means no hand-off, while the nudge that started the turn (typed before it) doesn't count.
func TestTurnEndHandoffSkipsANudgeTypedDuringTheTurn(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3510-0000-0000-000000000001"
	name := "claude-" + sid
	board := "team-3510"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "golf", WorkingDir: "/w", BoardName: &board}))
	tok := agentToken(t, h, sid, name)
	_, err := h.bs.Subscribe(ctx, board, "Worker", "Worker", name, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, board, "Lead", "Lead", "", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.PostMessage(ctx, board, "Lead", "@Worker mid-turn message", nil)
	require.NoError(t, err)

	ask := func() map[string]any {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/golf/turn-end", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	var typed time.Time
	var typedKey string
	prev := lastDelivered
	lastDelivered = func(n, key string) time.Time {
		if n != name || key != typedKey {
			return time.Time{}
		}
		return typed
	}
	t.Cleanup(func() { lastDelivered = prev })

	// No prompt_submit recorded and no nudge typed: the hand-off works as before.
	assert.Equal(t, true, ask()["handoff"], "nothing typed: hand off")

	event := func(typ string, at time.Time) {
		_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "golf", SessionID: &sid, EventType: typ, Summary: typ, CreatedAt: at.Format(store.ISOFormat)})
		require.NoError(t, err)
	}
	// An earlier turn (its prompt and its stop): only the current turn counts.
	turnStart := time.Now().UTC().Add(-10 * time.Second)
	event("prompt_submit", turnStart.Add(-60*time.Second))
	event("stop", turnStart.Add(-50*time.Second))
	event("prompt_submit", turnStart)

	// QA's repro: the nudge was typed 6 s into the turn and is queued in Claude's input.
	typedKey, typed = "board-unread:"+board, turnStart.Add(6*time.Second)
	assert.Equal(t, false, ask()["handoff"], "a nudge typed during the turn is already on its way: no second copy")
	// QA's live repro on Claude 2.1.300: Claude records a prompt_submit for the queued nudge
	// (0.4 s after it was typed). The turn still started at its FIRST prompt_submit.
	event("prompt_submit", typed.Add(400*time.Millisecond))
	assert.Equal(t, false, ask()["handoff"], "the queued nudge's own prompt_submit doesn't move the turn's start")

	// The nudge that STARTED this turn (typed just before its prompt_submit) doesn't count.
	typed = turnStart.Add(-500 * time.Millisecond)
	assert.Equal(t, true, ask()["handoff"], "the turn's own starting nudge: hand off the new messages")

	// A nudge for ANOTHER key typed during the turn doesn't stop it either.
	typedKey, typed = "ui-request:9", turnStart.Add(6*time.Second)
	assert.Equal(t, true, ask()["handoff"], "another key: hand off")
}

// #3510 end to end with the real gate: the turn starts (prompt_submit), the board nudge is
// TYPED into the agent mid-turn (the screen rule sends it; Claude queues it), and at the
// turn's end there is no hand-off, so the messages arrive once.
func TestTurnEndHandoffAfterARealTypedNudge(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3510-0000-0000-000000000002"
	name := "claude-" + sid
	board := "team-3510b"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "hotel", WorkingDir: "/w", BoardName: &board}))
	tok := agentToken(t, h, sid, name)
	_, err := h.bs.Subscribe(ctx, board, "Worker", "Worker", name, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, board, "Lead", "Lead", "", nil, "all")
	require.NoError(t, err)
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })

	_, err = h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "hotel", SessionID: &sid, EventType: "prompt_submit", Summary: "User submitted prompt",
		CreatedAt: time.Now().UTC().Add(-2 * time.Second).Format(store.ISOFormat)})
	require.NoError(t, err)

	_, err = h.bs.PostMessage(ctx, board, "Lead", "@Worker mid-turn message", nil)
	require.NoError(t, err)
	typed := false
	d, err := ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: idleBox{}, Name: name, AgentType: "claude", SessionID: sid,
		Key: "board-unread:" + board, Text: "You have 1 unread message", What: "test",
		Send: func(context.Context) error { typed = true; return nil }})
	require.NoError(t, err)
	require.True(t, typed, "the nudge was typed (%v)", d)
	require.False(t, ptymanager.LastDelivered(name, "board-unread:"+board).IsZero())
	// As Claude 2.1.300 does: a prompt_submit for the queued nudge, just after it was typed.
	_, err = h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "hotel", SessionID: &sid, EventType: "prompt_submit", Summary: "queued",
		CreatedAt: time.Now().UTC().Add(400 * time.Millisecond).Format(store.ISOFormat)})
	require.NoError(t, err)

	req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/hotel/turn-end", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.SessionTokenHeader, tok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, false, out["handoff"], "the typed nudge is queued in Claude: no second copy")
}
