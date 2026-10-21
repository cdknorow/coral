//go:build windows

package psquote

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
)

// argvEchoEnv makes this test binary print each argument it got, as the C runtime and Go
// read them, and exit (TestMain).
const argvEchoEnv = "CORAL_TEST_ARGV_ECHO"

func TestMain(m *testing.M) {
	if os.Getenv(argvEchoEnv) == "1" {
		for _, a := range os.Args[1:] {
			fmt.Printf("[%s]\n", a)
		}
		fmt.Print("--\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Each string goes through a script run by Windows PowerShell 5.1, as Coral's launch line
// writes it (Literal(NativeArg(s))), to a native program, which must get exactly s as one
// argument: the cases from #12 and a few hundred random strings of spaces, tabs, quotes and
// backslashes.
func TestNativeArgReachesTheProgramThroughPowerShell51(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no Windows PowerShell")
	}
	cases := []string{
		"a b", `a"b`, `a "b c" d`, `x=\"C:/Program Files (x86)/a.exe\"`, `C:\dir with space\`, `C:\nospace\`,
		`a\\"b`, `"`, `""`, `a b"`, `"a b"`, `a\b c`, `\\server\share x\`, `a\"`, `x y\\`, `ab"c d"`,
		`"a" "b"`, `x"`, "a\tb", `say "hi" now\`, `\`, `\\`, `a b\"`, `"a b\"`, `a"b c`,
		`hooks.SessionStart=[{hooks=[{type="command",command="\"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe\""}]}]`,
		`hooks.Stop=[{hooks=[{type="command",command="\"C:/Coral/coral-hook-agentic-state.exe\""}]}]`,
		`hooks.Stop=[{hooks=[{type="command",command="coral-hook-agentic-state --hooks-rev 3"}]}]`,
		"it's (x86) $env:PATH `whoami` ‘curly’",
	}
	rng := rand.New(rand.NewSource(12))
	const alphabet = ` "\a` + "\t"
	for i := 0; i < 300; i++ {
		b := make([]byte, 1+rng.Intn(9))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		cases = append(cases, string(b))
	}

	// The program is this test binary, whose argv is Go's; Go reads "" inside quotes the old
	// way (a quote that ends them), so the arguments NativeArg quotes itself go to Python
	// instead, whose argv is the C runtime's, as Codex's (Rust's) and node's are.
	var own, rest []string
	for _, s := range cases {
		if !wrapsNativeArg(s) && strings.ContainsAny(s, " \t") {
			own = append(own, s)
		} else {
			rest = append(rest, s)
		}
	}
	require.NotEmpty(t, own)
	roundTrip(t, ps, []string{os.Args[0]}, rest)
	if py, err := exec.LookPath("py.exe"); err == nil {
		roundTrip(t, ps, []string{py, "-c", "import sys\nfor a in sys.argv[1:]: print('[' + a + ']')\nprint('--')"}, own)
	} else {
		t.Log("no Python: the arguments NativeArg quotes itself aren't checked")
	}
}

// roundTrip passes each case to program through a script Windows PowerShell 5.1 runs, written
// as Coral's launch line writes an argument (Literal(NativeArg(s))), and checks the program got
// exactly that one argument.
func roundTrip(t *testing.T, ps string, program, cases []string) {
	t.Helper()
	var script strings.Builder
	script.WriteString(string(rune(0xfeff))) // a BOM: 5.1 reads the script as UTF-8
	for _, s := range cases {
		script.WriteString("&")
		for _, p := range program {
			script.WriteString(" " + Literal(p))
		}
		fmt.Fprintf(&script, " %s\n", Literal(NativeArg(s)))
	}
	file := filepath.Join(t.TempDir(), "args.ps1")
	require.NoError(t, os.WriteFile(file, []byte(script.String()), 0o600))
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file)
	cmd.Env = append(os.Environ(), argvEchoEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(out), "\r\n", "\n"), "--\n"), "--\n")
	require.Len(t, got, len(cases), "%s", out)
	for i, s := range cases {
		assert.Equal(t, "["+s+"]\n", got[i], "%q written as %q", s, NativeArg(s))
	}
}
