//go:build windows

package cliauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/internal/secfile/secfiletest"
)

// #3905: on Windows the command line checks the token's access list (Unix checks the mode and
// owner): a private file is read, one another account can read is refused.
func TestReadTokenChecksTheAccessList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TokenFile)
	require.NoError(t, os.WriteFile(path, []byte("tok\n"), 0o600))
	require.NoError(t, secfile.Protect(path))
	got, err := ReadToken(dir)
	require.NoError(t, err)
	assert.Equal(t, "tok", got)

	secfiletest.GrantUsersRead(t, path)
	_, err = ReadToken(dir)
	assert.ErrorIs(t, err, secfile.ErrExposed)
}
