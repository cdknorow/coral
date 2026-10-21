package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
)

// #60: on the PTY backend, what a person types in the dashboard terminal reaches the agent as it
// is, and the input debug log keeps only its length; Coral's own input is logged as before.
func TestPTYLogsAPersonsTypingByLengthOnly(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	b, _, proc := newFakeBackend(t, "claude-60")

	require.NoError(t, b.SendHumanInput("claude-60", []byte("hunter2")))
	require.NoError(t, b.SendHumanInput("claude-60", []byte("\r")))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "claude-60", []byte("coral's own")))
	assert.Equal(t, [][]byte{[]byte("hunter2"), []byte("\r"), []byte("coral's own")}, proc.written(), "the agent gets the input unchanged")

	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "hunter2")
	assert.Contains(t, text, `kind=typed text="[typed 7 bytes]"`)
	assert.Contains(t, text, `kind=keys keys="Enter"`, "an Enter is the key tmux would send (#60 step 4)")
	assert.Contains(t, text, `text="coral's own"`)
}

// #60 review N3: a person's text sent as a line (the dashboard's chat or send box) is logged by its
// length only, and its Enter as it is.
func TestPTYLogsAPersonsSentLineByLengthOnly(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	b, _, proc := newFakeBackend(t, "codex-60")
	require.NoError(t, b.SendLineCtx(context.Background(), terminalinput.Person, "codex-60", "my token is xyz"))
	assert.Equal(t, [][]byte{[]byte("my token is xyz"), []byte(EnterKey)}, proc.written())
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "my token")
	assert.Contains(t, string(raw), `kind=typed text="[typed 15 bytes]"`)
}

// #5673 (the merge of sets A and B): a person's text that goes in as a PASTE (a line break, or longer than a command typed into a shell) is logged by its length only too, as a typed line is.
// SendText's paste write took no ctx, so the input debug log kept the pasted text in full.
func TestPTYLogsAPersonsPastedTextByLengthOnly(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	b, _, proc := newFakeBackend(t, "claude-5673")
	require.NoError(t, b.SendText(context.Background(), terminalinput.Person, "claude-5673", "line one\nmy token is xyz-secret"))
	assert.NotEmpty(t, proc.written(), "the text was sent")
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secret", "a person's pasted text is not in the input log")
	assert.NotContains(t, string(raw), "line one")
	assert.Contains(t, string(raw), `kind=typed text="[typed `)
}

// #60 step 4: the PTY backend logs the same entries tmux logs for the same input: typed text as
// text and its Enter as a key, a control byte as tmux's key name, an escape sequence as raw bytes,
// multi-line text as a paste, a bracketed paste as the pasted text (never its ESC[200~ markers)
// and a key; nothing raw beyond what tmux logs as raw.
func TestPTYLogsTheEntriesTmuxLogs(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	b, s, _ := newFakeBackend(t, "codex-parity")
	ctx := context.Background()
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "codex-parity", []byte("ls -la\r")))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "codex-parity", []byte{0x03}))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "codex-parity", []byte("\x7f")))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "codex-parity", []byte("\x1b[A")))
	require.NoError(t, b.SendInput(terminalinput.Unclassified, "codex-parity", []byte("one\ntwo")))
	s.bracketedPaste.Store(true)
	require.NoError(t, b.SendPrompt(ctx, terminalinput.Unclassified, "codex-parity", "a prompt\nof two lines"))
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	text := string(raw)
	for _, want := range []string{
		`kind=text len=6 text="ls -la"`,
		`kind=keys keys="Enter"`,
		`kind=keys keys="C-c"`,
		`kind=keys keys="BSpace"`,
		`kind=raw len=3 text="\x1b[A"`,
		`kind=paste len=7 text="one\ntwo"`,
		`kind=paste len=21 text="a prompt\nof two lines"`,
	} {
		assert.Contains(t, text, want)
	}
	assert.NotContains(t, text, `200~`, "the bracketed-paste markers are not logged")
}
