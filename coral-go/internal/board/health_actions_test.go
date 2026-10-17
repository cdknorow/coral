package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4175, Security's #4176 rule 5: a cooldown per (rule, target), a cap per target per hour and
// a team budget per hour, all in the store.
func TestTakeHealthActionLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	lim := HealthLimits{Cooldown: 10 * time.Minute, PerTargetPerHour: 3, TeamPerHour: 5}
	take := func(rule, target string, at time.Time) string {
		why, err := s.TakeHealthAction(ctx, "team", rule, target, at, lim)
		require.NoError(t, err)
		return why
	}
	assert.Equal(t, "", take("claim", "Dev", t0))
	assert.Equal(t, HealthRefusedCooldown, take("claim", "Dev", t0.Add(5*time.Minute)))
	assert.Equal(t, "", take("status", "Dev", t0.Add(5*time.Minute)), "another rule has its own cooldown")
	assert.Equal(t, "", take("claim", "Dev", t0.Add(11*time.Minute)))
	assert.Equal(t, HealthRefusedTarget, take("waiting", "Dev", t0.Add(12*time.Minute)), "3 actions on Dev within the hour")
	assert.Equal(t, "", take("claim", "QA", t0.Add(13*time.Minute)))
	assert.Equal(t, "", take("claim", "UX", t0.Add(14*time.Minute)))
	assert.Equal(t, HealthRefusedBudget, take("claim", "Lead", t0.Add(15*time.Minute)), "the team's 5 an hour")
	assert.Equal(t, "", take("claim", "Lead", t0.Add(61*time.Minute)), "an hour later the budget is back")

	other, err := s.TakeHealthAction(ctx, "other-team", "claim", "Dev", t0.Add(15*time.Minute), lim)
	require.NoError(t, err)
	assert.Equal(t, "", other, "limits are per board")
}

// The escalate-after-N counters are stored: they survive a restart (a new Store on the same
// file), and a reset starts them over.
func TestHealthCountersSurviveARestart(t *testing.T) {
	path := t.TempDir() + "/board.db"
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now()
	c, err := s.BumpHealthCounter(ctx, "team", "claim", "Dev", "12", now)
	require.NoError(t, err)
	assert.Equal(t, 1, c.Count)
	c, err = s.BumpHealthCounter(ctx, "team", "claim", "Dev", "12", now)
	require.NoError(t, err)
	assert.Equal(t, 2, c.Count)
	require.NoError(t, s.MarkHealthEscalated(ctx, "team", "claim", "Dev", "12", now))
	require.NoError(t, s.Close())

	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	c, err = s.GetHealthCounter(ctx, "team", "claim", "Dev", "12")
	require.NoError(t, err)
	assert.Equal(t, 2, c.Count)
	assert.True(t, c.Escalated)
	assert.NotEmpty(t, c.LastAt)
	esc, err := s.HealthEscalations(ctx, "team")
	require.NoError(t, err)
	require.Len(t, esc, 1)
	assert.Equal(t, "Dev", esc[0].Target)

	require.NoError(t, s.ResetHealthCounter(ctx, "team", "claim", "Dev", "12"))
	c, err = s.GetHealthCounter(ctx, "team", "claim", "Dev", "12")
	require.NoError(t, err)
	assert.Equal(t, HealthCounter{}, c)
}

// #4176 rule 4: no agent can take a name that passes its words off as Coral's.
func TestCoralSystemAndHealthAreReserved(t *testing.T) {
	for _, n := range []string{"Coral", "coral", " CORAL ", "System", "Health", "Coral Task Queue"} {
		assert.True(t, IsReservedName(n), n)
	}
	for _, n := range []string{"Coral Dev", "Health Reviewer", "Systems Engineer"} {
		assert.False(t, IsReservedName(n), n)
	}
}
