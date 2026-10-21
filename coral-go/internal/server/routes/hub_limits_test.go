package routes

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3642: the Coral Hub pool sizes are whole numbers in range, or empty for the default.
func TestHubLimitSettingsAreValidated(t *testing.T) {
	for _, bad := range []map[string]any{
		{HubLimitGlobalSetting: 7.0}, {HubLimitGlobalSetting: 1025.0}, {HubLimitGlobalSetting: 32.5},
		{HubLimitLongSetting: 1.0}, {HubLimitLongSetting: "x"}, {HubLimitSearchSetting: 0.0},
		{HubLimitSearchSetting: 65.0}, {HubLimitSearchSetting: true},
	} {
		assert.Error(t, validateHubLimitSettings(bad), "%v", bad)
	}
	ok := map[string]any{HubLimitGlobalSetting: 128.0, HubLimitLongSetting: " 32 ", HubLimitSearchSetting: nil}
	require.NoError(t, validateHubLimitSettings(ok))
	assert.Equal(t, map[string]any{HubLimitGlobalSetting: "128", HubLimitLongSetting: "32", HubLimitSearchSetting: ""}, ok)
}

// The sizes are read from the settings for each request (at most a second old); anything
// missing or out of range is the default.
func TestHubLimitsFromSettings(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()

	get := HubLimitsFromSettings(ss)
	assert.Equal(t, tunnel.HubLimits{}, get(), "nothing set: the defaults")

	require.NoError(t, ss.SetSetting(ctx, HubLimitGlobalSetting, "100"))
	require.NoError(t, ss.SetSetting(ctx, HubLimitLongSetting, "9999"))
	require.NoError(t, ss.SetSetting(ctx, HubLimitSearchSetting, "8"))
	assert.Equal(t, tunnel.HubLimits{}, get(), "cached for a second")
	time.Sleep(1100 * time.Millisecond)
	assert.Equal(t, tunnel.HubLimits{Global: 100, Search: 8}, get(), "an out-of-range value is the default")
}
