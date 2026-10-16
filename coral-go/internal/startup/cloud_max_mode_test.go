package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// The hub access cap given at launch (--cloud-max-mode, CORAL_CLOUD_MAX_MODE): saved as the
// cloud_max_mode setting; unset changes nothing; an invalid value is an error, never a fallback.
func TestApplyCloudMaxMode(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		saved   string
		flag    string
		want    string // the cloud_max_mode setting afterwards ("" = not set)
		wantErr bool
	}{
		{"unset, nothing saved", "", "", "", false},
		{"unset leaves a saved cap", "interact", "", "interact", false},
		{"launch", "", "launch", "launch", false},
		{"launch raises a saved view", "view", "launch", "launch", false},
		{"view lowers a saved launch", "launch", "view", "view", false},
		{"interact", "", "interact", "interact", false},
		{"invalid is refused and changes nothing", "view", "root", "view", true},
		{"invalid with nothing saved saves nothing", "", "admin", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
			require.NoError(t, err)
			defer db.Close()
			ss := store.NewSessionStore(db)
			if tc.saved != "" {
				require.NoError(t, ss.SetSetting(ctx, routes.MaxModeSetting, tc.saved))
			}
			cfg := config.Load(dir)
			cfg.CloudMaxMode = tc.flag
			err = applyCloudMaxMode(ctx, db, cfg)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			settings, serr := ss.GetSettings(ctx)
			require.NoError(t, serr)
			assert.Equal(t, tc.want, settings[routes.MaxModeSetting])
		})
	}
}

// A cap below Launch ends the auto-accept Coral Hub turned on (it needs Launch), as lowering it
// in the dashboard does; one the owner turned on locally stays; Launch keeps both.
func TestCloudMaxModeBelowLaunchEndsHubAutoAccept(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		mode    string
		hubKept bool
	}{{"view", false}, {"interact", false}, {"launch", true}} {
		dir := t.TempDir()
		db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
		require.NoError(t, err)
		ss := store.NewSessionStore(db)
		for _, sid := range []string{"hub-aa", "local-aa"} {
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: sid, AgentType: "claude", WorkingDir: dir}))
		}
		_, err = ss.EnableAutoAccept(ctx, "hub-aa", store.AutoAcceptSourceHub, 0)
		require.NoError(t, err)
		_, err = ss.EnableAutoAccept(ctx, "local-aa", store.AutoAcceptSourceLocal, 0)
		require.NoError(t, err)

		cfg := config.Load(dir)
		cfg.CloudMaxMode = c.mode
		require.NoError(t, applyCloudMaxMode(ctx, db, cfg))
		on := func(sid string) bool {
			var v int
			require.NoError(t, db.GetContext(ctx, &v, "SELECT auto_accept FROM live_sessions WHERE session_id = ?", sid))
			return v == 1
		}
		assert.Equal(t, c.hubKept, on("hub-aa"), "%s: the hub's auto-accept", c.mode)
		assert.True(t, on("local-aa"), "%s: the owner's own auto-accept stays", c.mode)
		db.Close()
	}
}
