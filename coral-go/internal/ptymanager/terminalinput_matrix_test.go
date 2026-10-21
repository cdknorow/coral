//go:build !windows

package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// #5706 (Security's condition 3): the sanitising rule stops a message from typing keys, so it is proved on both backends with the same
// corpus and the same result: what reaches the terminal, as the input debug log records it, carries no key, and the two backends agree
// on what is delivered and on what is refused. Coral's text goes through PTYBackend.SendText and the tmux session terminal.
var injectionCorpus = []string{
	"plain text",
	"paste end \x1b[201~ rm -rf /",
	"paste start \x1b[200~ then",
	"ctrl-c \x03 and ctrl-d \x04",
	"cr\rlf\n crlf\r\n end",
	"osc \x1b]0;title\x07 set",
	"nul \x00 byte",
	"c1 \u009b31m csi \u0085 nel",
	"raw 9b \x9b byte",
	"lone \x1b escape",
	"del \x7f key",
	"tab\tseparated",
	"line one\nline two\nline three",
	"mixed \x1b[2J\x08\x1f text\nsecond \x1b[A line",
}

// controlOnly are texts with nothing left once the control characters are dropped: refused, never a bare Enter over a draft.
var controlOnly = []string{"\x03", "\x1b", "\x1b[201~\x04\x7f"[:1], "\u009b\u0080", "\n\x03\n", "\x1b\x04\x03", "\x07\x1b\x1f"}

var logLineRe = regexp.MustCompile(`kind=(\w+) (?:len=\d+ )?text=("(?:[^"\\]|\\.)*")`)

// deliveredText is the text a backend delivered, as its input log recorded it: the Raw, Text and Paste lines, without the Enter that
// submits it and without the bracketed-paste markers the PTY puts around a paste.
func deliveredText(t *testing.T, dir string) string {
	t.Helper()
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	var out strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		m := logLineRe.FindStringSubmatch(l)
		if m == nil {
			continue // keys="Enter"
		}
		s, err := strconv.Unquote(m[2])
		require.NoError(t, err, l)
		if s == "\r" {
			continue // the Enter that submits
		}
		s = strings.TrimPrefix(strings.TrimSuffix(s, "\x1b[201~"), "\x1b[200~")
		out.WriteString(s)
	}
	return out.String()
}

func assertNoKeys(t *testing.T, backend, in, got string) {
	t.Helper()
	assert.True(t, utf8.ValidString(got), "%s: %q is not valid UTF-8: a lone byte would reach the terminal as itself", backend, got)
	for _, r := range got {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			t.Errorf("%s: the text %q reached the terminal with the control character U+%04X: %q", backend, in, r, got)
		}
	}
}

func TestInjectionCorpusIsSafeAndTheSameOnBothBackends(t *testing.T) {
	withFastSubmit(t)
	term := newTmuxTestTerminal(t)
	const sid = "5706aaaa-0001-4000-8000-000000000001"
	name := "claude-" + sid
	spawnTmuxSession(t, term, name, t.TempDir())
	for _, in := range injectionCorpus {
		// PTY: a bracketed-paste agent; tmux: the same, by the hint the gate gives it.
		dir := t.TempDir()
		inputlog.Setup(dir, true)
		b, s, _ := newFakeBackend(t, "inj-pty")
		s.bracketedPaste.Store(true)
		require.NoError(t, b.SendText(context.Background(), terminalinput.Coral, "inj-pty", in), "%q", in)
		ptyGot := deliveredText(t, dir)
		inputlog.Setup("", false)

		dir = t.TempDir()
		inputlog.Setup(dir, true)
		ctx := tmux.WithBracketedPasteHint(context.Background(), true)
		require.NoError(t, term.SendInput(ctx, terminalinput.Coral, name, in, "claude", sid), "%q", in)
		tmuxGot := deliveredText(t, dir)
		inputlog.Setup("", false)
		inputlog.SetSetting(false)

		assert.NotEmpty(t, ptyGot, "the log recorded what the PTY got for %q", in)
		assert.NotEmpty(t, tmuxGot, "the log recorded what tmux got for %q", in)
		assertNoKeys(t, "pty", in, ptyGot)
		assertNoKeys(t, "tmux", in, tmuxGot)
		assert.Equal(t, ptyGot, tmuxGot, "the two backends deliver the same text for %q", in)
	}
}

func TestControlOnlyTextIsRefusedTheSameOnBothBackends(t *testing.T) {
	withFastSubmit(t)
	term := newTmuxTestTerminal(t)
	const sid = "5706aaaa-0002-4000-8000-000000000002"
	name := "claude-" + sid
	spawnTmuxSession(t, term, name, t.TempDir())
	for _, in := range controlOnly {
		for _, bracketed := range []bool{true, false} {
			b, s, proc := newFakeBackend(t, "inj-empty")
			s.bracketedPaste.Store(bracketed)
			err := b.SendText(context.Background(), terminalinput.Coral, "inj-empty", in)
			assert.ErrorIs(t, err, terminalinput.ErrNothingToSend, "pty %q bracketed=%v", in, bracketed)
			assert.Empty(t, proc.written(), "pty: no Enter, no empty paste for %q", in)
			ctx := tmux.WithBracketedPasteHint(context.Background(), bracketed)
			err = term.SendInput(ctx, terminalinput.Coral, name, in, "claude", sid)
			assert.ErrorIs(t, err, terminalinput.ErrNothingToSend, "tmux %q hint=%v", in, bracketed)
		}
	}
	// The unknown origin is refused the same way: it fails closed.
	b, _, proc := newFakeBackend(t, "inj-unknown")
	assert.ErrorIs(t, b.SendText(context.Background(), terminalinput.Unknown, "inj-unknown", "\x03"), terminalinput.ErrNothingToSend)
	assert.Empty(t, proc.written())
}

// A person's text is never changed (Prepare is a no-op for a person, on a typed line), on both backends.
func TestAPersonsTypedLineIsDeliveredAsItIsOnBothBackends(t *testing.T) {
	withFastSubmit(t)
	term := newTmuxTestTerminal(t)
	const sid = "5706aaaa-0003-4000-8000-000000000003"
	name := "claude-" + sid
	spawnTmuxSession(t, term, name, t.TempDir())
	in := "a line with a \x03 key and an \x1b[A arrow"
	b, _, proc := newFakeBackend(t, "inj-person")
	require.NoError(t, b.SendText(context.Background(), terminalinput.Person, "inj-person", in))
	require.NotEmpty(t, proc.written())
	assert.Equal(t, in, string(proc.written()[0]), "pty: a person's text is as typed")
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	require.NoError(t, term.SendInput(context.Background(), terminalinput.Person, name, in, "claude", sid))
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `text="[typed `, "tmux: logged by length only")
	assert.NotContains(t, string(raw), "arrow")
}
