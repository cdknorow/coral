package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// Hub mode at startup (#3460): the saved setting turns it on; --hub / CORAL_HUB=1 turn it
// on whatever is saved; nothing turns a forced one off.
func TestApplyHubModeSetting(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CORAL_HUB", "")
	for _, tc := range []struct {
		name          string
		saved         string
		forced        bool
		want, wantFor bool
	}{
		{"not set", "", false, false, false},
		{"saved off", "false", false, false, false},
		{"saved on", "true", false, true, false},
		{"--hub, saved off", "false", true, true, true},
		{"--hub, not set", "", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
			require.NoError(t, err)
			defer db.Close()
			if tc.saved != "" {
				require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, routes.HubModeSetting, tc.saved))
			}
			cfg := config.Load(dir)
			cfg.HubMode, cfg.HubModeForced = tc.forced, tc.forced
			applyHubModeSetting(ctx, db, cfg)
			assert.Equal(t, tc.want, cfg.HubMode)
			assert.Equal(t, tc.wantFor, cfg.HubModeForced)
		})
	}
	t.Setenv("CORAL_HUB", "1")
	cfg := config.Load(t.TempDir())
	assert.True(t, cfg.HubMode && cfg.HubModeForced, "CORAL_HUB=1 forces it")
}
