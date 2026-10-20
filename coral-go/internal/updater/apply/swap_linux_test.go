//go:build linux

package apply

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real atomic exchange, on this volume: renameat2(RENAME_EXCHANGE) swaps two folders in one step, twice for the rollback.
func TestSwapExchangesTwoFoldersAtomically(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "coral-bin"), filepath.Join(root, ".coral-bin.new")
	for p, v := range map[string]string{a: "old", b: "new"} {
		require.NoError(t, os.MkdirAll(p, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(p, "marker"), []byte(v), 0o644))
	}
	if !swapSupported(root) {
		t.Skip("this volume does not support RENAME_EXCHANGE")
	}
	require.NoError(t, swapPaths(b, a))
	got := func(p string) string { x, _ := os.ReadFile(filepath.Join(p, "marker")); return string(x) }
	assert.Equal(t, "new", got(a))
	assert.Equal(t, "old", got(b), "the old install is kept at the other name")
	require.NoError(t, swapPaths(b, a))
	assert.Equal(t, "old", got(a), "swapping again is the rollback")
	assert.Error(t, swapPaths(filepath.Join(root, "missing"), a))
	assert.Equal(t, "old", got(a))
	left, _ := os.ReadDir(root)
	assert.Len(t, left, 2, "no probe folder is left behind")
}
