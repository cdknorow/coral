package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

func TestIsResumeID(t *testing.T) {
	uuidV := "11111111-2222-4333-8444-555555555555"
	// Every type accepts a canonical UUID.
	for _, at := range []string{"claude", "agy", "gemini", "terminal", "pi", "codex", "", "unknown"} {
		assert.True(t, IsResumeID(at, uuidV), "type %q must accept a canonical UUID", at)
	}
	// A non-UUID that fits the safe charset is accepted only for codex (its native thread ids
	// aren't UUIDs; they run to 64 chars). Every other type — including an empty or unknown type,
	// which launches Claude, and antigravity, which resolves to agy — gets the UUID rule.
	codexID := "0199c3d2-6f3a-7bb1-codex-rollout-01HZX8Y3QK7-session-abcdef0123456789"[:64]
	assert.True(t, IsResumeID("codex", codexID), "codex must accept a safe non-UUID id")
	assert.True(t, IsResumeID("codex", "codex_thread-42"), "codex safe charset")
	for _, at := range []string{"claude", "agy", "gemini", "terminal", "pi", "antigravity", "", "unknown"} {
		assert.False(t, IsResumeID(at, codexID), "type %q must reject a non-UUID id", at)
	}
	// The anti-traversal boundary holds for EVERY type, UUID or not.
	for _, at := range []string{"claude", "codex", "", "pi", "unknown"} {
		for _, bad := range []string{
			"", "../x", "a/../../b", "/abs", "..", "a.b", "a/b",
			"-x", "--help", "-", "_x", "-codex",
			uuidV + "/../x", "urn:uuid:" + uuidV, "{" + uuidV + "}",
			uuidV + "\x00", "a b", strings.Repeat("a", 129),
		} {
			assert.False(t, IsResumeID(at, bad), "type %q must reject %q", at, bad)
		}
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
	linktest.Symlink(t, secretOutside, filepath.Join(srcProj, id, "link"))

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
