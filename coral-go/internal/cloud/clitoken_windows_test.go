//go:build windows

package cloud

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// #3905: the command-line token gets an access list of its own, and one another account can
// read is refused.
func TestCLITokenFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	tok, err := WriteCLIToken(dir)
	require.NoError(t, err)
	path := filepath.Join(dir, CLITokenFile)
	secfiletest.RequirePrivate(t, path)
	got, err := ReadCLIToken(dir)
	require.NoError(t, err)
	assert.Equal(t, tok, got)

	secfiletest.GrantUsersRead(t, path)
	_, err = ReadCLIToken(dir)
	assert.ErrorIs(t, err, secfile.ErrExposed)
}
