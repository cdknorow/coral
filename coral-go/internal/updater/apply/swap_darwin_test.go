//go:build darwin

package apply

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real atomic swap, on this volume: renamex_np(RENAME_SWAP) exchanges two folders in one step.
func TestSwapExchangesTwoBundlesAtomically(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "Coral.app"), filepath.Join(root, ".Coral.app.new")
	for p, v := range map[string]string{a: "old", b: "new"} {
		require.NoError(t, os.MkdirAll(p, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(p, "marker"), []byte(v), 0o644))
	}
	require.True(t, swapSupported(root), "the temp volume supports it")
	require.NoError(t, swapPaths(b, a))
	got := func(p string) string { x, _ := os.ReadFile(filepath.Join(p, "marker")); return string(x) }
	assert.Equal(t, "new", got(a))
	assert.Equal(t, "old", got(b), "the old install is kept at the other name, for the rollback")
	require.NoError(t, swapPaths(a, b))
	assert.Equal(t, "old", got(a), "and swapping again is the rollback")
	// A missing name is an error, not a half swap.
	assert.Error(t, swapPaths(filepath.Join(root, "missing"), a))
	assert.Equal(t, "old", got(a))
	left, _ := os.ReadDir(root)
	assert.Len(t, left, 2, "no probe folder is left behind")
}
