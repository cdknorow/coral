package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3601 W12: when the temp file can't be made, writeTempFile returns "" instead of writing
// to the predictable coral_<prefix>_<id>.<ext> in the shared temp folder, where another user
// could have planted a link first. A half-written file is removed, and the launch line
// leaves the argument out.
func TestWriteTempFileHasNoPredictableFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	const sid = "w12-session"

	// Someone else's file, and a link to it at the old fallback name.
	victim := filepath.Join(t.TempDir(), "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("UNCHANGED"), 0o600))
	planted := filepath.Join(tmp, "coral_prompt_"+sid+".txt")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(victim, planted))
	}

	createTemp = func(string, string) (*os.File, error) { return nil, errors.New("no space left") }
	t.Cleanup(func() { createTemp = os.CreateTemp })

	assert.Equal(t, "", writeTempFile("prompt", sid, "txt", []byte("PROMPT")))
	b, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "UNCHANGED", string(b), "nothing is written through the planted name")

	cmd := (&ClaudeAgent{}).BuildLaunchCommand(LaunchParams{SessionID: sid, Prompt: "do the thing", BoardName: "b", Role: "r"})
	assert.NotContains(t, cmd, "--settings", "no settings argument without a settings file")
	assert.NotContains(t, cmd, "$(cat", "no prompt argument without a prompt file")
	args, settings := (&ClaudeAgent{}).PrintArgs(LaunchParams{SessionID: sid})
	assert.Equal(t, "", settings)
	assert.NotContains(t, args, "--settings")
	codex := (&CodexAgent{}).BuildLaunchCommand(LaunchParams{SessionID: sid, Prompt: "do the thing", BoardName: "b", Role: "r"})
	assert.NotContains(t, codex, "$(cat", "codex: no instructions or prompt argument")

	// The write fails after the create: the half-written file is removed.
	var made string
	createTemp = func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		made = f.Name()
		f.Close()
		return os.Open(made) // read-only: the write fails
	}
	assert.Equal(t, "", writeTempFile("prompt", sid, "txt", []byte("PROMPT")))
	require.NotEmpty(t, made)
	_, err = os.Lstat(made)
	assert.True(t, os.IsNotExist(err), "the half-written file is removed")

	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), "coral_") && e.Name() != filepath.Base(planted), "left in the temp folder: %s", e.Name())
	}
}
