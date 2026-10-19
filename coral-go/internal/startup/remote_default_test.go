package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5124: Coral listens on this computer only by default; remote access (the saved setting or --remote) widens the default host to every
// interface, and a host that was asked for is never widened. The bind is decided before the listener opens.
func TestBindHostByRemoteAccessSetting(t *testing.T) {
	cases := []struct {
		name     string
		setting  string // "" = not saved
		force    bool
		host     string
		explicit bool
		want     string
	}{
		{"default, nothing saved", "", false, "127.0.0.1", false, "127.0.0.1"},
		{"default, off", "false", false, "127.0.0.1", false, "127.0.0.1"},
		{"default, junk value", "yes please", false, "127.0.0.1", false, "127.0.0.1"},
		{"default, on", "true", false, "127.0.0.1", false, "0.0.0.0"},
		{"default, on, any case", " TRUE ", false, "127.0.0.1", false, "0.0.0.0"},
		{"default, --remote", "", true, "127.0.0.1", false, "0.0.0.0"},
		{"asked-for wildcard, off", "", false, "0.0.0.0", true, "127.0.0.1"},
		{"asked-for wildcard, on", "true", false, "0.0.0.0", true, "0.0.0.0"},
		{"asked-for loopback, on stays loopback", "true", false, "127.0.0.1", true, "127.0.0.1"},
		{"asked-for tailnet address, on", "true", false, "100.64.0.1", true, "100.64.0.1"},
		{"asked-for tailnet address, off", "", false, "100.64.0.1", true, "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
			require.NoError(t, err)
			defer db.Close()
			if tc.setting != "" {
				require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, "remote_access_enabled", tc.setting))
			}
			cfg := &config.Config{Host: tc.host, HostExplicit: tc.explicit}
			applyPrivacySettings(ctx, db, cfg, tc.force, nil)
			assert.Equal(t, tc.want, cfg.Host)
		})
	}
}

// Settings that cannot be read never widen the bind, but --remote still does.
func TestBindHostWhenSettingsAreUnreadable(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	require.NoError(t, db.Close()) // a closed database fails the read

	cfg := &config.Config{Host: "0.0.0.0", HostExplicit: true}
	applyPrivacySettings(ctx, db, cfg, false, nil)
	assert.Equal(t, "127.0.0.1", cfg.Host)

	cfg = &config.Config{Host: "127.0.0.1"}
	applyPrivacySettings(ctx, db, cfg, true, nil)
	assert.Equal(t, "0.0.0.0", cfg.Host)
}
