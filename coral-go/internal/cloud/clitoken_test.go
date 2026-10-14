//go:build linux || darwin

package cloud

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server writes a fresh 0600 token at each start; only a file that passes
// the credential-file checks is read back; an empty token never matches.
func TestCLIToken(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	a, err := WriteCLIToken(dir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(a), 43, "256 bits")
	st, err := os.Stat(filepath.Join(dir, CLITokenFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	got, err := ReadCLIToken(dir)
	require.NoError(t, err)
	assert.Equal(t, a, got)
	assert.True(t, CLITokenMatches(a, got))

	b, err := WriteCLIToken(dir)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "a new token each start")
	assert.False(t, CLITokenMatches(b, a))
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no temporary file left behind")

	assert.False(t, CLITokenMatches("", ""))
	assert.False(t, CLITokenMatches(b, ""))
	assert.False(t, CLITokenMatches("", b))

	require.NoError(t, os.Chmod(filepath.Join(dir, CLITokenFile), 0o644))
	_, err = ReadCLIToken(dir)
	assert.ErrorIs(t, err, ErrUnsafeCredentialFile, "readable by others")
	empty := t.TempDir()
	require.NoError(t, os.Chmod(empty, 0o700)) // not 0775 under a umask of 002 (#3442)
	_, err = ReadCLIToken(empty)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// A server without a data directory (a test server, a bare config) has no
// command-line path: it never writes the token into the working directory,
// which once put a live token into the source tree (#3149 review).
func TestCLITokenNeedsADataDirectory(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	for _, dir := range []string{"", "  "} {
		tok, err := WriteCLIToken(dir)
		assert.Error(t, err)
		assert.Empty(t, tok)
	}
	entries, _ := os.ReadDir(wd)
	assert.Empty(t, entries, "nothing written in the working directory")
}

// A graceful shutdown removes the token this server wrote, never a newer
// server's (another start on the same data folder), and a missing file is fine.
func TestRemoveCLIToken(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	mine, err := WriteCLIToken(dir)
	require.NoError(t, err)
	require.NoError(t, RemoveCLIToken(dir, mine))
	assert.NoFileExists(t, filepath.Join(dir, CLITokenFile))
	assert.NoError(t, RemoveCLIToken(dir, mine), "already gone")

	_, err = WriteCLIToken(dir) // a newer server
	require.NoError(t, err)
	require.NoError(t, RemoveCLIToken(dir, mine))
	assert.FileExists(t, filepath.Join(dir, CLITokenFile), "not ours: kept")
	assert.NoError(t, RemoveCLIToken("", mine))
}

// #3442: a data folder made 0755 under a umask of 002 (Ubuntu's default) is 0775; the
// server drops the group and other write before writing the token, so the command line
// can read it back. A folder reached through a symlink works the same.
func TestCLITokenGroupWritableDataFolder(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o775))
	_, err := ReadCLIToken(dir)
	require.Error(t, err)
	tok, err := WriteCLIToken(dir)
	require.NoError(t, err)
	st, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())
	got, err := ReadCLIToken(dir)
	require.NoError(t, err)
	assert.Equal(t, tok, got)

	real := filepath.Join(t.TempDir(), "real")
	require.NoError(t, os.Mkdir(real, 0o777))
	require.NoError(t, os.Chmod(real, 0o777))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	tok, err = WriteCLIToken(link)
	require.NoError(t, err)
	got, err = ReadCLIToken(link)
	require.NoError(t, err)
	assert.Equal(t, tok, got)
	st, err = os.Stat(real)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())
}
