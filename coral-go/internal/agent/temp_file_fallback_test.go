package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3601 W12: when the temp file can't be made, writeTempFile returns an error naming the temp
// folder instead of writing to the predictable coral_<prefix>_<id>.<ext> there, where another
// user could have planted a link first. A half-written file is removed, and the launch fails
// with that error rather than starting an agent without its prompt, settings or instructions.
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

	p, err := writeTempFile("prompt", sid, "txt", []byte("PROMPT"))
	assert.Equal(t, "", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), os.TempDir(), "the error names the temp folder")
	b, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "UNCHANGED", string(b), "nothing is written through the planted name")

	params := LaunchParams{SessionID: sid, Prompt: "do the thing", BoardName: "b", Role: "r", CoralDir: t.TempDir()}
	for _, ag := range []Agent{&ClaudeAgent{}, &CodexAgent{}, &AgyAgent{}, &PiAgent{}} {
		cmd, err := ag.LaunchCommand(params)
		assert.Error(t, err, "%s launches without its files", ag.AgentType())
		assert.Equal(t, "", cmd, ag.AgentType())
	}
	args, settings, err := (&ClaudeAgent{}).PrintArgs(LaunchParams{SessionID: sid})
	assert.Error(t, err)
	assert.Nil(t, args)
	assert.Equal(t, "", settings)

	// The first file is made, the second fails: the launch fails and the first is removed.
	calls := 0
	createTemp = func(dir, pattern string) (*os.File, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("no space left")
		}
		return os.CreateTemp(dir, pattern)
	}
	_, err = (&ClaudeAgent{}).LaunchCommand(params)
	require.Error(t, err)
	require.Equal(t, 2, calls, "settings, then the prompt")

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
	_, err = writeTempFile("prompt", sid, "txt", []byte("PROMPT"))
	assert.Error(t, err)
	require.NotEmpty(t, made)
	_, err = os.Lstat(made)
	assert.True(t, os.IsNotExist(err), "the half-written file is removed")

	// Nothing is left in the temp folder but the planted link.
	entries, _ := os.ReadDir(tmp)
	var left []string
	for _, e := range entries {
		if e.Name() != filepath.Base(planted) {
			left = append(left, e.Name())
		}
	}
	assert.Empty(t, left, "files left in the temp folder")
}
