package ptymanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
)

// #5706 (the single terminal-input path, step 1): a golden matrix on the code as it was BEFORE the refactor. For each case it
// stores the bytes the PTY backend wrote to the process, the error it returned, and the exact input debug log lines (the
// timestamp aside, the target, source and kind stay). Every later commit of the refactor must leave testdata/terminalinput_golden_pty.txt unchanged.
// Regenerate (only on purpose, and say why in the commit) with CORAL_UPDATE_GOLDEN=1.
var goldenTimestampRe = regexp.MustCompile(`^\S+ target=`)

type goldenCase struct {
	name  string
	paste bool // the application enabled bracketed paste
	run   func(ctx context.Context, o terminalinput.Origin, b *PTYBackend, name string) error
	o     terminalinput.Origin // the explicit argument of #5706: Unclassified is what an unmarked context was
}

const (
	person    = terminalinput.Person
	automated = terminalinput.Coral
)

func goldenCases() []goldenCase {
	send := func(text string) func(context.Context, terminalinput.Origin, *PTYBackend, string) error {
		return func(ctx context.Context, o terminalinput.Origin, b *PTYBackend, name string) error {
			return b.SendText(ctx, o, name, text)
		}
	}
	prompt := func(text string) func(context.Context, terminalinput.Origin, *PTYBackend, string) error {
		return func(ctx context.Context, o terminalinput.Origin, b *PTYBackend, name string) error {
			return b.SendPrompt(ctx, o, name, text)
		}
	}
	line := func(text string) func(context.Context, terminalinput.Origin, *PTYBackend, string) error {
		return func(ctx context.Context, o terminalinput.Origin, b *PTYBackend, name string) error {
			return b.SendLineCtx(ctx, o, name, text)
		}
	}
	human := func(data string) func(context.Context, terminalinput.Origin, *PTYBackend, string) error {
		return func(_ context.Context, _ terminalinput.Origin, b *PTYBackend, name string) error {
			return b.SendHumanInput(name, []byte(data))
		}
	}
	plain := func(data string) func(context.Context, terminalinput.Origin, *PTYBackend, string) error {
		return func(_ context.Context, o terminalinput.Origin, b *PTYBackend, name string) error {
			return b.SendInput(o, name, []byte(data))
		}
	}
	secret := "my token is xyz-secret"
	multi := "line one\nline two: xyz-secret"
	return []goldenCase{
		{name: "person typed text", run: human("hunter2")},
		{name: "person Enter", run: human("\r")},
		{name: "person digits", run: human("123456")},
		{name: "person Ctrl-C", run: human("\x03")},
		{name: "person arrow key", run: human("\x1b[A")},
		{name: "person typed text with an arrow", run: human("ab\x1b[Ac")},
		{name: "coral raw input", run: plain("coral's own")},
		{name: "coral Enter", run: plain("\r")},
		{name: "person sent line", run: line(secret), o: person},
		{name: "coral sent line", run: line("echo hi")},
		{name: "person sent line, empty", run: line(""), o: person},
		{name: "person paste, bracketed paste on", paste: true, run: send(multi), o: person},
		{name: "person paste, bracketed paste off", run: send(multi), o: person},
		{name: "person long single line (a paste)", run: send(strings.Repeat("x", inlineCommandLimit+5)), o: person},
		{name: "person prompt, on", paste: true, run: prompt(multi), o: person},
		{name: "person prompt, off (refused)", run: prompt(multi), o: person},
		{name: "coral line with control characters", run: send("echo \x07hi\x1b[31m there"), o: automated},
		{name: "coral multi-line, bracketed paste on", paste: true, run: send("one\ntwo\x1b[201~three"), o: automated},
		{name: "coral multi-line, bracketed paste off", run: send("one\ntwo\x1b[201~three"), o: automated},
		{name: "coral CRLF paste, on", paste: true, run: send("one\r\ntwo"), o: automated},
		{name: "coral text of only control characters", run: send("\x07\x1b[31m\x1b"), o: automated},
		{name: "coral multi-line of only control characters, on", paste: true, run: send("\x07\n\x1b"), o: automated},
		{name: "coral prompt, on", paste: true, run: prompt("a prompt\nwith two lines\x1b[201~"), o: automated},
		{name: "coral prompt, off (refused)", run: prompt("a prompt"), o: automated},
		{name: "unmarked paste, on (today it is logged in full)", paste: true, run: send(multi)},
		{name: "unmarked sent line", run: line("plain text")},
	}
}

func runGolden(t *testing.T, c goldenCase) string {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	defer func() { inputlog.Setup("", false); inputlog.SetSetting(false) }()
	b, s, proc := newFakeBackend(t, "golden")
	s.bracketedPaste.Store(c.paste)
	withFastSubmit(t)
	o := c.o
	if o == terminalinput.Unknown {
		o = terminalinput.Unclassified
	}
	err := c.run(context.Background(), o, b, "golden")
	inputlog.Flush()
	var sb strings.Builder
	fmt.Fprintf(&sb, "== %s\n", c.name)
	for _, w := range proc.written() {
		fmt.Fprintf(&sb, "wrote %s\n", strconv.Quote(string(w)))
	}
	if err != nil {
		fmt.Fprintf(&sb, "error %v\n", err)
	}
	raw, rerr := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	if rerr == nil {
		for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			fmt.Fprintf(&sb, "log %s\n", goldenTimestampRe.ReplaceAllString(l, "target="))
		}
	}
	return sb.String()
}

func TestTerminalInputGoldenPTY(t *testing.T) {
	var all strings.Builder
	for _, c := range goldenCases() {
		all.WriteString(runGolden(t, c))
	}
	path := filepath.Join("testdata", "terminalinput_golden_pty.txt")
	if os.Getenv("CORAL_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(all.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "the golden file is part of the refactor's proof (#5706)")
	require.Equal(t, string(want), all.String(), "the PTY input path's bytes and log lines changed")
}
