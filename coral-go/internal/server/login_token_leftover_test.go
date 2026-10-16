package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/config"
)

// #3628 F7, Security's SHOULD: no login_token file outlives a run. A leftover is removed at
// startup even when no token is written, and the file is removed at a clean shutdown.
func TestLoginTokenFileLeftoversAreRemoved(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.UseCoralDir(dir)
	path := filepath.Join(dir, auth.LoginTokenFile)

	require.NoError(t, os.WriteFile(path, []byte("left by an earlier run\n"), 0o600))
	s := &Server{cfg: cfg} // no key store: no token to write
	token, err := s.WriteLoginToken()
	require.NoError(t, err)
	assert.Empty(t, token)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a leftover is removed at startup")

	ks, err := auth.NewKeyStore(t.TempDir())
	require.NoError(t, err)
	s = &Server{cfg: cfg, keyStore: ks}
	_, err = s.WriteLoginToken()
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err, "a running server's token file")
	s.RemoveLoginToken() // a clean shutdown
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "removed at shutdown")
}
