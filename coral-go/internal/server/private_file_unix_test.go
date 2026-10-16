//go:build !windows

package server

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requirePrivateFile: on Unix a secret file is private by its mode.
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), path)
}
