//go:build windows

package agent

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/psquote"
)

// #29 (Security's #20 F1): a prompt, and Codex's developer_instructions, reach the CLI as one
// argument through the launch line Windows PowerShell 5.1 runs, read back from the file
// promptArg wrote, whatever the text: an odd number of quotes before its first space no longer
// splits it into options. The cases are the review's, the earlier ones of #12, and a few
// hundred random strings of spaces, tabs, quotes, backslashes and dashes.
func TestPromptArgReachesTheProgramThroughPowerShell51(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no Windows PowerShell")
	}
	t.Setenv("CORAL_SHELL", ps)
	cases := []string{
		`"Review --sandbox danger-full-access`,
		`a"b c --x`,
		`say "hi" to C:\dir\ and "a b\"`,
		`C:\dir\`, `C:\dir with space\`, `"`, `x"`, `--dangerously-skip-permissions`,
		"héllo wörld 日本語 🎉 — \"q\"", "line one\nline \"two\" --x\n",
		`it's $env:PATH $(calc) ` + "`whoami`",
	}
	rng := rand.New(rand.NewSource(29))
	const alphabet = ` "\a-` + "\t"
	for i := 0; i < 200; i++ {
		b := make([]byte, 1+rng.Intn(12))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		cases = append(cases, string(b))
	}

	// This test binary's argv is Go's, which reads "" inside quotes the old way, so the values
	// NativeArg quotes itself go to Python (the C runtime's argv, as Codex's and node's are).
	var own, rest []string
	for _, s := range cases {
		if strings.HasPrefix(psquote.NativeArg(s), `"`) { // quoted by NativeArg, not by 5.1
			own = append(own, s)
		} else {
			rest = append(rest, s)
		}
	}
	require.NotEmpty(t, own)
	promptRoundTrip(t, ps, []string{os.Args[0]}, rest)
	if py, err := exec.LookPath("py.exe"); err == nil {
		promptRoundTrip(t, ps, []string{py, "-c", "import sys\nfor a in sys.argv[1:]: print('[' + a + ']')\nprint('--')"}, own)
	} else {
		t.Log("no Python: the arguments NativeArg quotes itself aren't checked")
	}
}

// promptRoundTrip passes each case to program as a prompt argument, and as a Codex
// developer_instructions one, through a script Windows PowerShell 5.1 runs, and checks the
// program got exactly that argument.
func promptRoundTrip(t *testing.T, ps string, program, cases []string) {
	t.Helper()
	files := &launchFiles{}
	var script strings.Builder
	script.WriteString(string(rune(0xfeff)))
	var want []string
	for _, s := range cases {
		for _, lead := range []string{"", "developer_instructions="} {
			script.WriteString("&")
			for _, p := range program {
				script.WriteString(" " + psquote.Literal(p))
			}
			fmt.Fprintf(&script, " %s\n", files.promptArg("prompt29", "t", "txt", lead, s))
			want = append(want, lead+s)
		}
	}
	_, err := files.done("")
	require.NoError(t, err)
	defer func() {
		for _, p := range files.made {
			os.Remove(p)
		}
	}()
	file := filepath.Join(t.TempDir(), "prompts.ps1")
	require.NoError(t, os.WriteFile(file, []byte(script.String()), 0o600))
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file)
	cmd.Env = append(os.Environ(), argvEchoEnv+"=1", "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(out), "\r\n", "\n"), "--\n"), "--\n")
	require.Len(t, got, len(want), "%s", out)
	for i, w := range want {
		assert.Equal(t, "["+w+"]\n", got[i], "%q", w)
	}
}
