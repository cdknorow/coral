package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5419: Coral that could not be started again once it has ended (no record of how it was started, no service manager) is not offered a relaunch, and one that
// is asked for anyway is refused with 409 before the updater is started, which is what ends Coral.
func TestNoRelaunchIsOfferedOrStartedWhenCoralCouldNotBeStartedAgain(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA) // a token made while a restart was possible
	require.NotEmpty(t, tok)
	e.h.CanRestart = func() bool { return false }

	code, st := e.status(hubHdr(ownerA))
	require.Equal(t, 200, code)
	rl := st["relaunch"].(map[string]any)
	assert.Equal(t, false, rl["available"])
	assert.Equal(t, "no_run_record", rl["reason"])
	assert.NotContains(t, rl, "ack", "no token is issued for a relaunch that cannot happen")

	code, out := e.post(`{"ack":"`+tok+`"}`, hubHdr(ownerA))
	assert.Equal(t, 409, code, "%v", out)
	assert.Equal(t, "no_run_record", out["code"])
	assert.Empty(t, e.up.applies, "the updater was not started: Coral was not ended")
	last := e.up.audits[len(e.up.audits)-1]
	assert.Equal(t, "no_run_record", last.Code)

	// The same request with a restart possible starts it, so the refusal was the restart's.
	e.h.CanRestart = func() bool { return true }
	tok = e.token(ownerA)
	code, out = e.post(`{"ack":"`+tok+`"}`, hubHdr(ownerA))
	assert.Equal(t, 202, code, "%v", out)
	assert.Len(t, e.up.applies, 1)
}

func TestARestartThatFailedAfterARollbackIsInTheStatus(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.Staged, e.up.rec.State = nil, updNone
	e.up.rec.NotReady = "up_to_date"
	e.up.rec.LastRollback = &UpdateRollback{At: e.now, From: "1.4.130", To: "1.4.131", Reason: "not_healthy", RestartFailed: true}
	_, st := e.status(hubHdr(ownerA))
	lr := st["last_rollback"].(map[string]any)
	assert.Equal(t, true, lr["restart_failed"])
	assert.Equal(t, "not_healthy", lr["reason"])

	e.up.rec.LastRollback.RestartFailed = false
	_, st = e.status(hubHdr(ownerA))
	assert.NotContains(t, st["last_rollback"].(map[string]any), "restart_failed", "absent unless true")
}
