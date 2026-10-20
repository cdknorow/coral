package background

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5229: an agent whose CLI says its quota is used up is BLOCKED in the check, with the reset time, not "idle" (it was counted idle, and named as an
// idle agent that needs work).
func TestHealthCheckShowsAQuotaBlockedAgentAsBlocked(t *testing.T) {
	h := newCheckHarness(t)
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Idler", "Developer", 4, 40*time.Minute)
	h.member("Quota", "Developer", 5, 40*time.Minute) // awake, no turn, no wait, no work: idle by its hooks
	quotaSID := fmt.Sprintf("%08d-0000-0000-0000-000000000000", 5)

	before := h.check()
	assert.Contains(t, before, "2 idle", "without a limit both are idle")
	assert.NotContains(t, before, "BLOCKED")

	h.m.limit = func(_ context.Context, a AgentInfo) (time.Duration, bool) {
		if a.SessionID == quotaSID {
			return 103*time.Hour + 13*time.Minute + 54*time.Second, true
		}
		return 0, false
	}
	out := h.check()
	require.Contains(t, out, "1 BLOCKED by a usage limit", out)
	assert.Contains(t, out, "1 idle", "the other is still idle")
	assert.NotContains(t, out, "2 idle")
	assert.Contains(t, out, "Blocked by a usage limit, 1: Quota (resets in 103 h 13 min)")
	assert.NotContains(t, out, "Quota: idle", "a blocked agent is not named as one that needs work")
	assert.Contains(t, out, "Idler: idle", "an idle one still is")
	assert.False(t, strings.Contains(out, "\n"))

	// A limit with no time shown.
	h.m.limit = func(_ context.Context, a AgentInfo) (time.Duration, bool) { return 0, a.SessionID == quotaSID }
	assert.Contains(t, h.check(), "Quota (reset time not shown)")
}

type screenRT struct {
	AgentRuntime
	screen string
}

func (r screenRT) AgentScreen(context.Context, string, string, string) (string, error) {
	return r.screen, nil
}

// screenLimit reads agy's quota notice off its screen, only for agy, and only when the runtime can show a screen.
func TestScreenLimitReadsAgyQuotaOnly(t *testing.T) {
	const quota = "⚠ Individual quota reached. Please upgrade. Resets in\n103h13m54s.\nError ID: x\n\n How's the CLI experience so far? Help us improve:\n [1] Good  [2] Fine  [3] Bad  [0] Skip\n"
	f := screenLimit(screenRT{screen: quota})
	require.NotNil(t, f)
	resets, blocked := f(context.Background(), AgentInfo{AgentType: "agy", SessionID: "s1"})
	assert.True(t, blocked)
	assert.Equal(t, 103*time.Hour+13*time.Minute+54*time.Second, resets)
	_, blocked = f(context.Background(), AgentInfo{AgentType: "claude", SessionID: "s1"})
	assert.False(t, blocked, "other agent types report limits through their transcripts")
	idle := screenLimit(screenRT{screen: "● Done.\n\n> \n"})
	_, blocked = idle(context.Background(), AgentInfo{AgentType: "agy", SessionID: "s1"})
	assert.False(t, blocked)
	assert.Nil(t, screenLimit(nilScreenRT{}), "a runtime with no screen sees no limit")
}

type nilScreenRT struct{ AgentRuntime }
