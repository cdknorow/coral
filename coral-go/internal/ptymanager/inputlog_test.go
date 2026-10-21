package ptymanager

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
)

// Every write to a PTY is recorded with the code path that sent it, and Ctrl-D
// reaches coral.log even with the debug file off (#3256).
func TestPTYWritesAreRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The input-debug.log handle is still open when t.TempDir's RemoveAll runs, and Windows
		// can't unlink an open file, so cleanup fails the test. Input recording is exercised on
		// unix; see the #3629 report note on whether the debug-log handle should close sooner.
		t.Skip("Windows can't unlink the still-open input-debug.log at TempDir cleanup (#3629)")
	}
	var coralLog logBuf
	old := log.Writer()
	log.SetOutput(&coralLog)
	t.Cleanup(func() { log.SetOutput(old) })

	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false) })
	withFastSubmit(t)

	b, sess, _ := newFakeBackend(t, "claude-pty1")
	sess.bracketedPaste.Store(true) // the CLI asked for bracketed paste, so the prompt is pasted
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "claude-pty1", []byte("ls\r")))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "claude-pty1", []byte("\x04")))
	ctx := context.Background()
	require.NoError(t, b.SendPrompt(ctx, terminalinput.Unclassified, "claude-pty1", "do the task"))

	inputlog.Flush()
	data, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	got := string(data)
	// The PTY backend logs what the tmux backend logs for the same input (#60 step 4): the typed line, then Enter, and Ctrl-D as a key.
	assert.Contains(t, got, `target=claude-pty1 source=ptymanager.TestPTYWritesAreRecorded kind=text len=2 text="ls"`)
	assert.Contains(t, got, `kind=keys keys="Enter"`)
	assert.Contains(t, got, `kind=keys keys="C-d"`)
	assert.Contains(t, got, "do the task", "the prompt write is recorded too")
	assert.NotContains(t, coralLog.String(), "kind=", "input goes to the debug file only, never coral.log")
}
