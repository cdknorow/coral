package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4157: an API key that can't be read is an error the caller shows (the tray's start-failure
// menu), not an exit from inside the constructor.
func TestNewWithKeyReturnsTheAPIKeyError(t *testing.T) {
	cfg := newE2EConfig(t, t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "api_key"), 0o700)) // a folder, not a key file
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)

	srv, err := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	require.Error(t, err)
	assert.Nil(t, srv)
	assert.Contains(t, err.Error(), "API key")
}
