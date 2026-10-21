package tmux

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

const longTextSID = "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"

func tempHandoffs(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), "coral-command-*"))
	return m
}

// #4361: text for an agent is delivered AS TEXT at any length. Over 900 characters it was turned
// into a script and the agent received ". '/tmp/coral-command-N.sh'" instead of the message.
func TestSendKeysDeliversLongTextAsTextNeverAScript(t *testing.T) {
	skipWithoutUnixTmux(t)
	for name, text := range map[string]string{
		"5 KB multi-line":                strings.Repeat("a line of the operator's long message\n", 140) + "end",
		"5 KB on one line":               strings.Repeat("one long line of the message ", 180) + "end",
		"just over the old limit":        strings.Repeat("x", 901),
		"multi-line, with shell in it":   "$(touch /tmp/pwned)\n`id`\n; rm -rf ~ #\n" + strings.Repeat("y", 1000),
		"quotes and a leading dash line": "'\"\\\n- fix the tests\n--help\n" + strings.Repeat("z", 1000),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			c, dir := fakeTmux(t)
			require.NoError(t, c.SendKeys(WithBracketedPasteHint(context.Background(), true), terminalinput.Unclassified, "agent", text, "claude", longTextSID))

			var kinds []string
			calls := readCalls(t, dir)
			for _, call := range calls {
				kinds = append(kinds, strings.Fields(call)[0])
				assert.NotContains(t, call, ". '", "no line that sources a file is typed")
				assert.NotContains(t, call, "coral-command-")
			}
			assert.Contains(t, kinds, "load-buffer")
			assert.Contains(t, kinds, "paste-buffer", "delivered as a paste")
			got, err := os.ReadFile(filepath.Join(dir, "stdin.txt"))
			require.NoError(t, err)
			assert.Equal(t, text, string(got), "the exact text reaches the pane")
			assert.Empty(t, tempHandoffs(t), "no script file was made")
		})
	}
}

// A short single line is still typed with send-keys -l, as before.
func TestSendKeysTypesAShortLine(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	require.NoError(t, c.SendKeys(context.Background(), terminalinput.Unclassified, "agent", "hello there", "claude", longTextSID))
	joined := strings.Join(readCalls(t, dir), "\n")
	assert.Contains(t, joined, "send-keys -t "+promptTestSession+":0.0 -l -- hello there")
	assert.NotContains(t, joined, "paste-buffer")
}

// A COMMAND Coral composed (SendKeysToTarget: a launch line) over the limit still goes through a
// script, which is removed when the send fails; nothing is left behind.
func TestLaunchCommandWrapIsRemovedOnFailure(t *testing.T) {
	skipWithoutUnixTmux(t)
	t.Setenv("TMPDIR", t.TempDir())
	c, dir := fakeTmux(t)
	cmd := "echo " + strings.Repeat("x", inlineCommandLimit+100)

	// The typing fails: the script is removed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fail-enter"), nil, 0o600))
	err := c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, promptTestSession+":0.0", cmd)
	require.Error(t, err, "the typing failed")
	assert.Empty(t, tempHandoffs(t), "the script is removed when the send fails")
	assert.Contains(t, strings.Join(readCalls(t, dir), "\n"), "coral-command-", "a long command was handed over as a script")

	// The typing works and only the Enter fails: removed too. And with the Enter withheld.
	require.NoError(t, os.Remove(filepath.Join(dir, "fail-enter")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fail-only-enter"), nil, 0o600))
	err = c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, promptTestSession+":0.0", cmd)
	require.Error(t, err, "the Enter failed")
	assert.Empty(t, tempHandoffs(t), "the script is removed when the Enter fails")
	require.NoError(t, os.Remove(filepath.Join(dir, "fail-only-enter")))
	err = c.SendKeysToTarget(WithBeforeEnter(context.Background(), func(context.Context) bool { return false }), terminalinput.Unclassified, promptTestSession+":0.0", cmd)
	require.ErrorIs(t, err, ErrEnterWithheld)
	assert.Empty(t, tempHandoffs(t), "the script is removed when the Enter is withheld")
}

// Only launch code hands a command over as a script: commandInvocation has one caller per package
// (the shared typing function, with launch set, and the PTY launch send), the agent-text methods do
// not reach it, and SendKeysToTarget, which can wrap, is called only from launch paths.
func TestOnlyLaunchPathsWrapACommand(t *testing.T) {
	root := filepath.Join("..", "..")
	invocations := map[string]int{}
	sendKeysToTarget := map[string]int{}
	callRe := regexp.MustCompile(`\bcommandInvocation\(`)
	sendRe := regexp.MustCompile(`\.SendKeysToTarget\(`)
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.Base(path), "command_delivery") {
			return nil
		}
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if n := len(callRe.FindAll(b, -1)); n > 0 {
			invocations[rel] = n
		}
		if n := len(sendRe.FindAll(b, -1)); n > 0 {
			sendKeysToTarget[rel] = n
		}
		return nil
	}))
	assert.Equal(t, map[string]int{
		"internal/tmux/send_keys.go":                  1, // typeToTarget, only when launch is set
		"internal/ptymanager/session_terminal_pty.go": 1, // PTYSessionTerminal.SendToTarget (a launch command)
	}, invocations, "a new caller of commandInvocation must be a launch path: say so here")
	assert.Equal(t, map[string]int{
		"cmd/launch-coral/main.go":                     1,
		"internal/background/runtime_tmux.go":          1,
		"internal/ptymanager/tmux_backend.go":          2,
		"internal/ptymanager/session_terminal_tmux.go": 1,
	}, sendKeysToTarget, "a new caller of SendKeysToTarget (which wraps a long command) must be a launch path: say so here")

	// And the agent-text method itself never reaches the wrapping function.
	body := funcBody(t, "send_keys.go", "func (c *Client) SendKeys(")
	assert.NotContains(t, body, "SendKeysToTarget(")
	assert.NotContains(t, body, "commandInvocation(")
	assert.Contains(t, body, "typeToTarget(ctx, o, target, command, false)")
}

// The dashboard's /send reaches a tmux agent through TmuxSessionTerminal.SendInput: it calls the text
// method (SendKeys), never the launch typing function.
func TestTmuxSessionTerminalSendInputIsTheTextPath(t *testing.T) {
	path := filepath.Join("..", "ptymanager", "session_terminal_tmux.go")
	body := funcBody(t, path, "func (t *TmuxSessionTerminal) SendInput(_ terminalinput.Origin, ")
	assert.Contains(t, body, "t.client.SendKeys(")
	assert.NotContains(t, body, "SendKeysToTarget(")
	launch := funcBody(t, path, "func (t *TmuxSessionTerminal) SendToTarget(_ terminalinput.Origin, ")
	assert.Contains(t, launch, "SendKeysToTarget(", "the launch send is the one that may wrap")
}

// funcBody is the text of the function that begins with signature in the Go source at path: from the
// signature to the closing brace at column 0. CRLF line endings (a Windows checkout) are normalized
// first, and every index is checked, so a miss fails the test with a message and never panics.
func funcBody(t *testing.T, path, signature string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(src, signature)
	require.GreaterOrEqual(t, i, 0, "%s: no %q", path, signature)
	n := strings.Index(src[i:], "\n}\n")
	require.GreaterOrEqual(t, n, 0, "%s: %q has no closing brace at column 0", path, signature)
	return src[i : i+n]
}
