package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsResumeID(t *testing.T) {
	assert.True(t, IsResumeID("11111111-2222-4333-8444-555555555555"))
	for _, bad := range []string{
		"", "../x", "a/../../b", "/abs", "..",
		"11111111-2222-4333-8444-555555555555/../x",
		"urn:uuid:11111111-2222-4333-8444-555555555555", // uuid.Parse accepts it; we don't
		"{11111111-2222-4333-8444-555555555555}",        // braces
		"11111111222243338444555555555555",              // no hyphens
		"11111111-2222-4333-8444-555555555555\x00",      // trailing NUL
	} {
		assert.False(t, IsResumeID(bad), "%q must be rejected", bad)
	}
}

// PrepareResume with a non-canonical session id does no filesystem I/O (#3582): the guard
// returns before it reads a source or creates the target project directory.
func TestPrepareResumeRejectsTraversalIDs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	// A source file a bad id could reach, under an existing project entry. Without the guard,
	// PrepareResume finds it and creates the target project dir before failing; with the guard
	// it returns first, so that dir is never created.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "p", "sub", "evil.jsonl"), []byte("x"), 0o644))
	// A real session file one directory above the base, the kind a "../" id would reach.
	outside := filepath.Join(filepath.Dir(base), "escaped.jsonl")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	t.Cleanup(func() { os.Remove(outside) })

	a := &ClaudeAgent{}
	for _, id := range []string{"sub/evil", "../escaped", "..", "a/../../b", "/abs", "x/../../../etc/passwd"} {
		a.PrepareResume(id, "/work/dir")
	}
	// The target project dir ("-work-dir") was never created, and nothing new is under the base
	// but the "p" source we planted. The outside file is untouched.
	_, err := os.Stat(filepath.Join(base, "-work-dir"))
	assert.True(t, os.IsNotExist(err), "PrepareResume created the target dir for a bad id")
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "p", entries[0].Name())
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))
}

// A valid id copies the session file (and its dir), and writeNoFollow refuses a planted symlink.
func TestPrepareResumeValidIDCopiesAndWriteNoFollowRefusesSymlink(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	id := "11111111-2222-4333-8444-555555555555"
	srcProj := filepath.Join(base, "some-other-project")
	require.NoError(t, os.MkdirAll(filepath.Join(srcProj, id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcProj, id+".jsonl"), []byte("transcript"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcProj, id, "a.txt"), []byte("file"), 0o644))
	// A symlink inside the session dir must not be copied (skipped as non-regular). It points at
	// a file that exists, so the only reason it isn't copied is the non-regular skip.
	secretOutside := filepath.Join(t.TempDir(), "outside-secret")
	require.NoError(t, os.WriteFile(secretOutside, []byte("secret-contents"), 0o600))
	require.NoError(t, os.Symlink(secretOutside, filepath.Join(srcProj, id, "link")))

	a := &ClaudeAgent{}
	a.PrepareResume(id, "/work-abc")

	target := filepath.Join(base, "-work-abc")
	got, err := os.ReadFile(filepath.Join(target, id+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "transcript", string(got))
	got, err = os.ReadFile(filepath.Join(target, id, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "file", string(got))
	_, err = os.Lstat(filepath.Join(target, id, "link"))
	assert.True(t, os.IsNotExist(err), "a symlink in the source dir must not be copied")

	// writeNoFollow refuses to write through a symlink planted at the target.
	victim := filepath.Join(base, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("original"), 0o600))
	planted := filepath.Join(base, "planted")
	require.NoError(t, os.Symlink(victim, planted))
	assert.Error(t, writeNoFollow(planted, []byte("overwrite")), "writeNoFollow followed a symlink")
	data, _ := os.ReadFile(victim)
	assert.Equal(t, "original", string(data))
}
