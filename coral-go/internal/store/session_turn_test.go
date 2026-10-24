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
}
