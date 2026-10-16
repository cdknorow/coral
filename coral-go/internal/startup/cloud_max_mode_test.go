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
