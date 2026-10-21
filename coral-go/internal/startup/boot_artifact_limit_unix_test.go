//go:build !windows

package startup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
)

// #4602/#4678: Start raises an unset artifact limit once when the store already holds 80% of the 20 GiB default. The routes tests cover the
// function; this pins that Start calls it (removing the call from Start fails nothing else). The store is one sparse 17 GiB object.
func TestStartRaisesAnUnsetArtifactLimitOverASeventeenGiBStore(t *testing.T) {
	const gib = int64(1) << 30
	var home string
	bootWithStubCLIs(t, func(ctx context.Context, db *store.DB, h string) {
		home = h
		seedSparseArtifact(t, h, 17*gib)
	})
	settings := bootedSettings(t, home)
	assert.Equal(t, strconv.FormatInt(34*gib, 10), settings[routes.ArtifactStoreMaxBytesSetting], "twice the store's size")
	assert.Equal(t, settings[routes.ArtifactStoreMaxBytesSetting], settings[routes.ArtifactStoreAutoRaisedSetting], "recorded, for the Settings note")
}

// An Operator's own limit, 0 included, is not touched by a start.
func TestStartLeavesAnOperatorsArtifactLimitAlone(t *testing.T) {
	const gib = int64(1) << 30
	var home string
	bootWithStubCLIs(t, func(ctx context.Context, db *store.DB, h string) {
		home = h
		seedSparseArtifact(t, h, 17*gib)
		require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, routes.ArtifactStoreMaxBytesSetting, "0"))
	})
	settings := bootedSettings(t, home)
	assert.Equal(t, "0", settings[routes.ArtifactStoreMaxBytesSetting])
	assert.Empty(t, settings[routes.ArtifactStoreAutoRaisedSetting])
}

// seedSparseArtifact makes one object of the given size (a sparse file: no disk is used) in home's artifact store.
func seedSparseArtifact(t *testing.T, home string, size int64) {
	t.Helper()
	dir := filepath.Join(home, "artifacts", "objects")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	f, err := os.Create(filepath.Join(dir, strings.Repeat("ab", 32)))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

// bootedSettings reads the settings of the server bootWithStubCLIs started, through a second connection to its database.
func bootedSettings(t *testing.T, home string) map[string]string {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(home, "sessions.db"), "")
	require.NoError(t, err)
	defer db.Close()
	s, err := store.NewSessionStore(db).GetSettings(ctx)
	require.NoError(t, err)
	return s
}
