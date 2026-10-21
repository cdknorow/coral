package tmux

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
)

// Every way Coral types into a tmux pane is recorded with its target and the code
// path that sent it (here the test, past the tmux plumbing), and the keys that end
// a CLI also reach coral.log (#3256).
func TestTmuxInputIsRecorded(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "6d1e2f30-4a5b-4c6d-8e7f-90a1b2c3d4e5"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	target := name + ".0"

	var coralLog bytes.Buffer
	old := log.Writer()
	log.SetOutput(&coralLog)
	t.Cleanup(func() { log.SetOutput(old) })
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false) })

	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, "echo one"))
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, "-t x --help")) // after "--", all text (#3690)
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, "line a\nline b"))
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "\x1b[A"))
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "\x03"))
	require.NoError(t, c.SendRawKeys(ctx, terminalinput.Unclassified, name, []string{"Escape"}, "claude", sid))
	_ = c.SendPrompt(WithBracketedPasteHint(ctx, true), terminalinput.Unclassified, name, "the prompt text", "claude", sid)

	inputlog.Flush()
	data, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	got := string(data)
	src := "source=tmux.TestTmuxInputIsRecorded "
	for _, want := range []string{
		"target=" + target + " " + src + `kind=text len=8 text="echo one"`,
		"target=" + target + " " + src + `kind=text len=11 text="-t x --help"`,
		"target=" + target + " " + src + `kind=keys keys="Enter"`,
		"target=" + target + " " + src + `kind=paste len=13 text="line a\nline b"`,
		"target=" + target + " " + src + `kind=raw len=3 text="\x1b[A"`,
		"target=" + target + " " + src + `kind=keys keys="C-c"`,
		src + `kind=keys keys="Escape"`,
		src + `kind=paste len=15 text="the prompt text"`,
	} {
		assert.Contains(t, got, want)
	}
	// The standard logger is process-wide: a line another test's background work wrote while this one ran (an agent priority "kind=tree" line from an earlier killed agent) is not this test's input.
	// Only a line that names this test's own target can be its input leaking into coral.log (#5684).
	for _, line := range strings.Split(coralLog.String(), "\n") {
		assert.NotContains(t, line, "target="+target, "input goes to the debug file only, never coral.log")
	}
}

// #5706: recording is done by sendKeys, the one place send-keys is issued: run records nothing, so a command that is not input
// (list-panes, capture-pane) leaves no line, and a send-keys that skipped sendKeys would leave none either (the census catches it).
func TestOnlySendKeysRecords(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, _ := fakeTmux(t)
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false) })
	_, _ = c.run(context.Background(), "list-panes", "-a")
	_, _ = c.run(context.Background(), "capture-pane", "-t", "x", "-p")
	inputlog.Flush()
	_, err := os.Stat(filepath.Join(dir, inputlog.FileName))
	assert.True(t, os.IsNotExist(err), "nothing was written")
	_, _ = c.sendKeys(context.Background(), terminalinput.Coral, "-t", "x", "-l", "--", "hi")
	inputlog.Flush()
	data, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	assert.Contains(t, string(data), `target=x `)
	assert.Contains(t, string(data), `text="hi"`)
}

// #60: on the tmux backend, a person's own input (terminalinput.Person) is logged by its length only,
// literal or as hex bytes; key names stay; hex is logged decoded, never as a hex copy.
func TestTmuxLogsAPersonsTypingByLengthOnly(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	recordSendKeys(terminalinput.Person, []string{"-t", "%1", "-l", "--", "hunter2"})
	recordSendKeys(terminalinput.Person, []string{"-t", "%1", "-H", "68", "-H", "69"}) // "hi"
	recordSendKeys(terminalinput.Person, []string{"-t", "%1", "--", "Enter"})
	recordSendKeys(terminalinput.Person, []string{"-t", "%1", "--", "C-c"})
	recordSendKeys(terminalinput.Person, []string{"-t", "%1", "--", "s"})                    // a password typed through /keys
	recordSendKeys(terminalinput.Unclassified, []string{"-t", "%1", "-H", "6f", "-H", "6b"}) // "ok"
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "hunter2")
	assert.Contains(t, text, `kind=typed text="[typed 7 bytes]"`)
	assert.Contains(t, text, `kind=typed text="[typed 2 bytes]"`)
	assert.Contains(t, text, `keys="Enter"`)
	assert.Contains(t, text, `keys="C-c"`)
	assert.NotContains(t, text, `keys="s"`, "a character typed through /keys on tmux is hidden like the PTY's (#5715)")
	assert.Contains(t, text, `kind=typed text="[typed 1 bytes]"`)
	assert.Contains(t, text, `kind=raw len=2 text="ok"`)
	assert.NotContains(t, text, "6f")
}

// #5682 (found in the sweep of the assembled 148 tree): a person's multi-line or long text from the dashboard send box is PASTED into the pane (pasteToTarget), and that record took no context, so
// the input debug log kept the text (after secret redaction, cut at 200 bytes) where a typed line is logged by its length only (#60 N3). Same class as #5673 on the PTY backend.
func TestTmuxLogsAPersonsPastedTextByLengthOnly(t *testing.T) {
	c := newTestClient(t)
	const sid = "7e2f3041-5b6c-4d7e-9f80-a1b2c3d4e5f6"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	target := name + ".0"
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })

	require.NoError(t, c.SendKeysToTarget(context.Background(), terminalinput.Person, target, "line one\nmy token is xyz-secret"))
	require.NoError(t, c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, target, "not a person\nsent by Coral"))
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "xyz-secret", "a person's pasted text is not in the input log")
	assert.NotContains(t, text, "line one")
	assert.Contains(t, text, `kind=typed text="[typed 31 bytes]"`)
	assert.Contains(t, text, `kind=paste len=26 text="not a person\nsent by Coral"`, "what Coral itself sends is still logged")
}
