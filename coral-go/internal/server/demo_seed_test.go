package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// A server seeds the demo workflow and its scheduled job by default; with
// CORAL_NO_DEMO_SEED=1 (test servers) it seeds neither, so it never runs a
// real agent on its own (#3136).
func TestDemoSeedCanBeTurnedOff(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{{"", 1}, {"1", 0}, {"true", 0}, {"0", 1}} {
		t.Run("CORAL_NO_DEMO_SEED="+tc.env, func(t *testing.T) {
			t.Setenv("CORAL_NO_DEMO_SEED", tc.env)
			cfg := newE2EConfig(t, t.TempDir())
			db, err := storetest.Open(t, cfg.DBPath)
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			stack := newTermStack(t)
			New(cfg, db, stack.backend, stack.terminal)

			ctx := context.Background()
			var jobs, workflows int
			require.NoError(t, db.GetContext(ctx, &jobs, `SELECT COUNT(*) FROM scheduled_jobs WHERE name = 'morning-motivation'`))
			require.NoError(t, db.GetContext(ctx, &workflows, `SELECT COUNT(*) FROM workflows WHERE name = 'daily-motivation'`))
			assert.Equal(t, tc.want, jobs, "seeded scheduled job")
			assert.Equal(t, tc.want, workflows, "seeded workflow")
		})
	}
}
