package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #7090: how a session's current turn stands, read from its stored events in order.
func TestSessionTurnFollowsTheStoredEvents(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ctx := t.Context()
	post := func(sid, typ, detail string) {
		ev := &AgentEvent{AgentName: "a", EventType: typ, SessionID: &sid}
		if detail != "" {
			ev.DetailJSON = &detail
		}
		_, err := ts.InsertAgentEvent(ctx, ev)
		require.NoError(t, err)
	}
	state := func(sid string) RunTurn {
		got, err := ts.SessionTurn(ctx, sid)
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, TurnNone, state("none").State, "no events")

	post("s1", "stop", "") // a stop with no prompt before it ends nothing (a startup, a reset)
	assert.Equal(t, TurnNone, state("s1").State)
	post("s1", "prompt_submit", "")
	assert.Equal(t, TurnWorking, state("s1").State)
	post("s1", "tool_use", "")
	post("s1", "permission_request", "")
	assert.Equal(t, TurnWorking, state("s1").State, "a request is a turn still going")
	post("s1", "stop", `{"failed":false}`)
	got := state("s1")
	assert.Equal(t, TurnDone, got.State)
	assert.NotEmpty(t, got.At)
	post("s1", "tool_use", "") // the agent went on after the stop: its turn did not end there
	assert.Equal(t, TurnWorking, state("s1").State)
	post("s1", "stop", "")
	assert.Equal(t, TurnDone, state("s1").State)
	post("s1", "prompt_submit", "") // another prompt: a new turn
	assert.Equal(t, TurnWorking, state("s1").State)
	post("s1", "session_reset", "")
	assert.Equal(t, TurnNone, state("s1").State, "a reset starts over")
	post("s1", "stop", "")
	assert.Equal(t, TurnNone, state("s1").State, "and a stop after it, with no prompt, ends nothing")

	post("s2", "prompt_submit", "")
	post("s2", "stop", `{"failed":true,"error":"overloaded"}`)
	assert.Equal(t, TurnFailed, state("s2").State, "a StopFailure is a failed turn")
	post("s2", "session_end", "")
	assert.Equal(t, TurnFailed, state("s2").State, "the session ending after it does not turn it into a success")

	post("s3", "prompt_submit", "")
	post("s3", "notification", "")
	assert.Equal(t, TurnWorking, state("s3").State, "a notification is not an end")
	assert.Equal(t, TurnDone, func() string { post("s3", "stop", "not json"); return state("s3").State }(), "a detail that does not parse is not a failure")
	assert.Equal(t, TurnNone, state("s4").State, "another session is its own")

	// A turn with hundreds of tool calls reads as it should, from the newest of them: still going, then ended by the stop after them.
	post("s5", "prompt_submit", "")
	for i := 0; i < 400; i++ {
		post("s5", "tool_use", "")
	}
	assert.Equal(t, TurnWorking, state("s5").State)
	post("s5", "stop", "")
	assert.Equal(t, TurnDone, state("s5").State)
}

// The read is bounded: it asks for the one newest prompt or reset and the one newest event after it, not the session's rows (QA's note on #7090).
func TestSessionTurnReadsOnlyTheNewestRows(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ctx := t.Context()
	sid := "s-bounded"
	for _, typ := range []string{"prompt_submit", "tool_use", "tool_use", "tool_use", "stop"} {
		_, err := ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a", EventType: typ, SessionID: &sid})
		require.NoError(t, err)
	}
	var n int
	require.NoError(t, db.Get(&n, `SELECT COUNT(*) FROM agent_events WHERE session_id = ?`, sid))
	require.Equal(t, 5, n)
	got, err := ts.SessionTurn(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, TurnDone, got.State)
	// What the newest read leaves out does not matter: with the old tool calls gone, the answer is the same.
	_, err = db.Exec(`DELETE FROM agent_events WHERE session_id = ? AND event_type = 'tool_use' AND id NOT IN (SELECT MAX(id) FROM agent_events WHERE session_id = ? AND event_type = 'tool_use')`, sid, sid)
	require.NoError(t, err)
	again, err := ts.SessionTurn(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, got, again)
}
