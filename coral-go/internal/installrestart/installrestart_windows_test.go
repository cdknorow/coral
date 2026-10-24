//go:build windows

package installrestart

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func inTemp(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "Coral")
	old := folder
	folder = func() (string, error) { return dir, nil }
	t.Cleanup(func() { folder = old })
	return dir
}

// A mark from the install that just closed Coral starts it once; nothing, a second take, an old mark (a logoff hours ago) or one from the
// future start nothing, and every take removes the file.
func TestARecentMarkStartsCoralOnceAndNothingElseDoes(t *testing.T) {
	dir := inTemp(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	assert.False(t, Take(now), "no mark")

	require.NoError(t, Mark(now.Add(-5*time.Minute)))
	assert.True(t, Take(now), "the install that closed Coral")
	assert.False(t, Take(now), "once")

	require.NoError(t, Mark(now.Add(-2*time.Hour)))
	assert.False(t, Take(now), "a stale mark")
	_, err := os.Stat(filepath.Join(dir, fileName))
	assert.True(t, os.IsNotExist(err), "and it is gone")

	require.NoError(t, Mark(now.Add(time.Hour)))
	assert.False(t, Take(now), "a mark from the future")

	require.NoError(t, os.WriteFile(filepath.Join(dir, fileName), []byte("not a time"), 0o600))
	assert.False(t, Take(now), "a mark that is not a time")
}

// The real folder is this user's Local AppData known folder, then Coral (a scratch USERPROFILE moves the known folder with it, QA on #335).
func TestTheMarkLivesInThisUsersLocalAppData(t *testing.T) {
	dir, err := folder()
	require.NoError(t, err)
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DONT_VERIFY) // a scratch profile may have none yet
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(local, "Coral"), dir)
}

// The mark is created new, never written through what already has its name (Security on #335: the helper may run elevated): a hard link
// to another file planted there is removed as itself, and that file keeps its bytes.
func TestMarkNeverWritesThroughWhatHasItsName(t *testing.T) {
	dir := inTemp(t)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	other := filepath.Join(t.TempDir(), "someone-elses-file")
	require.NoError(t, os.WriteFile(other, []byte("keep me"), 0o600))
	require.NoError(t, os.Link(other, filepath.Join(dir, fileName)))

	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Mark(now))
	b, err := os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, "keep me", string(b))
	assert.True(t, Take(now), "the new mark is there")
}

// A mark folder that is not a plain folder (here a file; a link or a junction alike) is refused, never written into.
func TestMarkRefusesAFolderThatIsNotAPlainFolder(t *testing.T) {
	dir := inTemp(t)
	require.NoError(t, os.WriteFile(dir, []byte("x"), 0o600))
	assert.Error(t, Mark(time.Now()))
}
