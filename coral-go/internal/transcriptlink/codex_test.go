package transcriptlink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

// #3597 (weak site W19): the link folder is one the Codex agent can write, so a link's Path is
// believed only when it is that thread's own rollout file inside <CODEX_HOME>/sessions, and the
// link file itself is read as a small regular file.

const (
	sid    = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	thread = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
)

func codexHomeFor(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "coral-session-links"), 0o700))
	return home
}

func writeLink(t *testing.T, home, path string) {
	t.Helper()
	b, _ := json.Marshal(CodexLink{ThreadID: thread, Path: path})
	require.NoError(t, os.WriteFile(filepath.Join(home, "coral-session-links", sid+".json"), b, 0o600))
}

func rollout(t *testing.T, dir, name string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
	return p
}

func TestCodexLinkPathMustBeTheThreadsOwnRollout(t *testing.T) {
	home := codexHomeFor(t)
	day := filepath.Join(home, "sessions", "2026", "10", "15")
	good := rollout(t, day, "rollout-2026-10-15T10-00-00-"+thread+".jsonl")

	writeLink(t, home, good)
	link := Codex(sid)
	assert.Equal(t, thread, link.ThreadID)
	realGood, _ := filepath.EvalSymlinks(good)
	assert.Equal(t, realGood, link.Path, "the thread's own rollout file is believed")

	outside := filepath.Join(t.TempDir(), "rollout-x-"+thread+".jsonl")
	require.NoError(t, os.WriteFile(outside, []byte("secret\n"), 0o600))
	other := rollout(t, day, "rollout-2026-10-15T10-00-00-00000000-0000-4000-8000-000000000000.jsonl")
	notes := rollout(t, day, "notes.txt")
	link2 := filepath.Join(day, "rollout-link-"+thread+".jsonl")
	linktest.Symlink(t, outside, link2)

	for name, p := range map[string]string{
		"outside the sessions folder":            outside,
		"another thread's rollout":               other,
		"not a rollout file":                     notes,
		"a symlink in sessions pointing outside": link2,
		"a relative path":                        "sessions/2026/10/15/" + filepath.Base(good),
		"a UNC path":                             `\\host\share\rollout-x-` + thread + ".jsonl",
		"a traversal":                            filepath.Join(day, "..", "..", "..", "..", filepath.Base(outside)),
	} {
		writeLink(t, home, p)
		link := Codex(sid)
		assert.Equal(t, thread, link.ThreadID, name)
		assert.Empty(t, link.Path, "%s: the Path is dropped", name)
	}
}
