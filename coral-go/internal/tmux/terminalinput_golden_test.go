package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/terminalinput"
)

// #5706 (the single terminal-input path, step 1): a golden matrix on the tmux backend as it was BEFORE the refactor, against the
// real tmux the package tests use. For each case it stores the error the send returned and the exact input debug log lines (the
// timestamp aside; the target is replaced; source and kind stay). The bytes tmux delivers are not stored: they pass through
// send-keys arguments, whose log lines are the record. (A text over the inline limit is left out: SendKeysToTarget hands it over as a script file whose name, and so the logged length, differs per run.) Every later commit of the refactor must leave
// testdata/terminalinput_golden_tmux.txt unchanged. Regenerate (only on purpose) with CORAL_UPDATE_GOLDEN=1.
var goldenStampRe = regexp.MustCompile(`^\S+ target=\S+ `)

func TestTerminalInputGoldenTmux(t *testing.T) {
	c := newTestClient(t)
	const sid = "6d1e2f30-4a5b-4c6d-8e7f-90a1b2c3d4e6"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	target := name + ".0"
	// hint: 0 none, 1 bracketed paste known on, 2 known off. The origin is the 5706 explicit argument: Person, Coral, or
	// Unclassified for the senders nobody has classified (what an unmarked context was).
	const (
		noHint = iota
		hintOn
		hintOff
	)
	P, C, U := terminalinput.Person, terminalinput.Coral, terminalinput.Unclassified
	secret, multi := "my token is xyz-secret", "line one\nline two: xyz-secret"
	keys := func(text string) goldenOp { return goldenOp{"keys", text, nil} }
	term := func(data string) goldenOp { return goldenOp{"term", data, nil} }
	prompt := func(text string) goldenOp { return goldenOp{"prompt", text, nil} }
	raw := func(k ...string) goldenOp { return goldenOp{"raw", "", k} }
	cases := []struct {
		name string
		o    terminalinput.Origin
		hint int
		run  goldenOp
	}{
		{"person typed text", P, noHint, term("hunter2")},
		{"person Enter", P, noHint, term("\r")},
		{"person digits", P, noHint, term("123456")},
		{"person Ctrl-C", P, noHint, term("\x03")},
		{"person arrow key", P, noHint, term("\x1b[A")},
		{"person typed text with an arrow", P, noHint, term("ab\x1b[Ac")},
		{"coral terminal input", U, noHint, term("coral's own")},
		{"person sent line", P, noHint, keys(secret)},
		{"coral sent line", U, noHint, keys("echo hi")},
		{"person multi-line (a paste)", P, noHint, keys(multi)},
		{"person prompt, hint on", P, hintOn, prompt(multi)},
		{"person prompt, hint off", P, hintOff, prompt(multi)},
		{"person prompt, hint unknown", P, noHint, prompt(multi)},
		{"coral line with control characters", C, noHint, keys("echo \x07hi\x1b[31m there")},
		{"coral multi-line", C, noHint, keys("one\ntwo\x1b[201~three")},
		{"coral CRLF paste", C, noHint, keys("one\r\ntwo")},
		{"coral text of only control characters", C, noHint, keys("\x07\x1b[31m\x1b")},
		{"coral multi-line of only control characters", C, noHint, keys("\x07\n\x1b")},
		{"coral prompt, hint on", C, hintOn, prompt("a prompt\nwith two lines\x1b[201~")},
		{"coral prompt, hint off", C, hintOff, prompt("a prompt")},
		{"coral prompt, hint unknown", C, noHint, prompt("a prompt")},
		{"unmarked multi-line (today it is logged in full)", U, noHint, keys(multi)},
		{"unmarked prompt, hint on", U, hintOn, prompt(multi)},
		{"named keys", U, noHint, raw("Escape", "C-c")},
		{"person named keys", P, noHint, raw("Up")},
	}
	var all strings.Builder
	for _, tc := range cases {
		dir := t.TempDir()
		inputlog.Setup(dir, true)
		ctx := context.Background()
		if tc.hint != noHint {
			ctx = WithBracketedPasteHint(ctx, tc.hint == hintOn)
		}
		err := goldenSend(ctx, c, tc.o, target, name, sid, tc.run)
		inputlog.Flush()
		inputlog.Setup("", false)
		inputlog.SetSetting(false)
		fmt.Fprintf(&all, "== %s\n", tc.name)
		if err != nil {
			fmt.Fprintf(&all, "error %v\n", err)
		}
		if b, rerr := os.ReadFile(filepath.Join(dir, inputlog.FileName)); rerr == nil {
			for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				fmt.Fprintf(&all, "log %s\n", goldenStampRe.ReplaceAllString(l, "target=T "))
			}
		}
	}
	path := filepath.Join("testdata", "terminalinput_golden_tmux.txt")
	if os.Getenv("CORAL_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(all.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "the golden file is part of the refactor's proof (#5706)")
	require.Equal(t, string(want), all.String(), "the tmux input path's log lines changed")
}

// goldenOp is one send of the matrix; goldenSend runs it, so the debug log's source= is this one function for every case.
type goldenOp struct {
	kind string
	text string
	keys []string
}

func goldenSend(ctx context.Context, c *Client, o terminalinput.Origin, target, name, sid string, op goldenOp) error {
	switch op.kind {
	case "keys":
		return c.SendKeysToTarget(ctx, o, target, op.text)
	case "term":
		return c.SendTerminalInputToTarget(ctx, o, target, op.text)
	case "prompt":
		return c.SendPrompt(ctx, o, name, op.text, "claude", sid)
	default:
		return c.SendRawKeys(ctx, o, name, op.keys, "claude", sid)
	}
}
