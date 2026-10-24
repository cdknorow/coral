package routes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/sessionstate"
)

// waiting_since is when the request began, only while the agent waits and the start is known (#6981).
func TestWaitingSinceIsTheRequestsStartOnlyWhileWaiting(t *testing.T) {
	at := time.Date(2026, 10, 24, 7, 30, 0, 0, time.FixedZone("x", 3600))
	assert.Equal(t, "2026-10-24T06:30:00Z", waitingSince(sessionstate.State{NeedsInput: true, Since: at}), "UTC")
	assert.Nil(t, waitingSince(sessionstate.State{NeedsInput: false, Since: at}), "not waiting")
	assert.Nil(t, waitingSince(sessionstate.State{NeedsInput: true}), "start not known")
}
