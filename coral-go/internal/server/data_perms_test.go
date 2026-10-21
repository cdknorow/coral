package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3552, #3628 F6: a data folder an older Coral left world-readable is made this user's only at
// startup: folders 0700, the databases, the logs, artifact files and the rest 0600. A symlink is
// never followed, and a tighter mode is never loosened.
func TestTightenDataPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	mk := func(rel string, mode os.FileMode) string {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), mode))
		require.NoError(t, os.Chmod(p, mode))
		return p
	}
	files := []string{
		mk("sessions.db", 0o644), mk("messageboard.db", 0o644), mk("messageboard.db-wal", 0o644),
		mk("coral.log", 0o644), mk("artifacts/objects/abc", 0o644), mk("artifacts/objects/abc.json", 0o644),
		mk("artifacts/uploads/sess/abc.json", 0o644), mk("artifacts/sessions/s1/changes.diff", 0o644),
		mk("uploads/abc_shot.png", 0o644), // #3602 W28: the dashboard's uploads too
		// #3628 F6: the other logs, the rotated copies, the generated team, the lock, older board
		// state files, and the knowledge, workflow, pi and theme folders' files.
		mk("coral.log.1", 0o644), mk("app.log", 0o644), mk("tray.log", 0o644),
		mk("last_generated_team.json", 0o644), mk("coral-server.lock", 0o644),
		mk("board_state_claude-0000.json", 0o644),
		mk("agent_knowledge/team/index.md", 0o644), mk("workflows/runs/r1/step.txt", 0o644),
		mk("pi-sessions/s1/session.jsonl", 0o644), mk("themes/dark.json", 0o644),
	}
	tight := mk("artifacts/objects/def", 0o400)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "artifacts", "objects", "link")))

	tightenDataPermissions(dir)

	mode := func(p string) os.FileMode { fi, err := os.Stat(p); require.NoError(t, err); return fi.Mode().Perm() }
	assert.Equal(t, os.FileMode(0o700), mode(dir))
	for _, d := range []string{"artifacts", "artifacts/objects", "artifacts/uploads", "artifacts/uploads/sess", "artifacts/sessions/s1", "uploads",
		"agent_knowledge", "agent_knowledge/team", "workflows", "workflows/runs", "workflows/runs/r1", "pi-sessions", "pi-sessions/s1", "themes"} {
		assert.Equal(t, os.FileMode(0o700), mode(filepath.Join(dir, d)), d)
	}
	for _, f := range files {
		assert.Equal(t, os.FileMode(0o600), mode(f), f)
	}
	assert.Equal(t, os.FileMode(0o400), mode(tight), "a tighter mode stays")
	assert.Equal(t, os.FileMode(0o644), mode(outside), "a symlink's target is never touched")

	// A second start changes nothing.
	tightenDataPermissions(dir)
	assert.Equal(t, os.FileMode(0o600), mode(files[0]))
}

// Security's #3552 nit (c): a data folder reached through a symlink has its real folder
// tightened.
func TestTightenDataPermissionsThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	real := filepath.Join(t.TempDir(), "real-data")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "artifacts", "objects"), 0o755))
	require.NoError(t, os.Chmod(real, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(real, "sessions.db"), []byte("x"), 0o644))
	link := filepath.Join(t.TempDir(), "coral-link")
	require.NoError(t, os.Symlink(real, link))

	tightenDataPermissions(link)

	fi, err := os.Stat(real)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	fi, err = os.Stat(filepath.Join(real, "sessions.db"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	fi, err = os.Stat(filepath.Join(real, "artifacts", "objects"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

// Any file directly in the data folder, not only the known databases and logs, is made 0600 (#96): a key or token file an older Coral left.
func TestTightenDataPermissionsCoversRootFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "api_key")
	require.NoError(t, os.WriteFile(key, []byte("k"), 0o644))
	require.NoError(t, os.Chmod(key, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "session_tokens"), 0o755))
	tok := filepath.Join(dir, "session_tokens", "s1")
	require.NoError(t, os.WriteFile(tok, []byte("t"), 0o644))
	require.NoError(t, os.Chmod(tok, 0o644))

	tightenDataPermissions(dir)

	for _, p := range []string{key, tok} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), p)
	}
}
