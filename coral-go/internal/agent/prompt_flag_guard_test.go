package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const argvPrinterSource = `package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if in, err := io.ReadAll(os.Stdin); err == nil && len(in) > 0 {
		os.WriteFile(os.Getenv("ARGV_OUT")+".stdin", in, 0o600)
	}
	out, err := os.Create(os.Getenv("ARGV_OUT"))
	if err != nil {
		os.Exit(3)
	}
	defer out.Close()
	for _, a := range os.Args[1:] {
		fmt.Fprintf(out, "%q\n", a)
	}
}
`

var (
	argvPrinterOnce sync.Once
	argvPrinterPath string
	argvPrinterErr  error
)

// argvPrinter is a program that writes each of its arguments, quoted, one per line to the file named by ARGV_OUT:
// what a CLI's parser would be handed.
func argvPrinter(t *testing.T) string {
	t.Helper()
	argvPrinterOnce.Do(func() {
		dir, err := os.MkdirTemp("", "coral-argvprinter")
		if err != nil {
			argvPrinterErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		argvPrinterPath = filepath.Join(dir, "argvprinter")
		if runtime.GOOS == "windows" {
			argvPrinterPath += ".exe"
		}
		if argvPrinterErr = os.WriteFile(src, []byte(argvPrinterSource), 0o644); argvPrinterErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", argvPrinterPath, src)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GO111MODULE=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			argvPrinterErr = fmt.Errorf("go build: %v: %s", err, out)
		}
	})
	if argvPrinterErr != nil {
		t.Fatalf("can't build the argv printer: %v", argvPrinterErr)
	}
	return argvPrinterPath
}

// runLaunchLine runs a launch command the way Coral's shell does: PowerShell on Windows, /bin/sh elsewhere.
func runLaunchLine(t *testing.T, line string) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell.exe")
		if err != nil {
			t.Skip("no Windows PowerShell")
		}
		cmd = exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", line)
	} else {
		cmd = exec.Command("/bin/sh", "-c", line)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s\n%s", line, out)
}

// #36 (Security's #29 note 2): a prompt that is only an option ("--dangerously-skip-permissions") or starts like
// one ("-c x=y", "@file") reaches every agent's CLI as the prompt. The launch line is really run, against a program
// that records its arguments; the argument holding the prompt is either after "--" (claude, codex), the value of
// -i (agy), or does not start with "-" or "@" (pi, which has no "--").
func TestAnOptionLikePromptReachesEveryCLIAsThePrompt(t *testing.T) {
	printer := argvPrinter(t)
	if runtime.GOOS != "windows" {
		t.Setenv("SHELL", "/bin/sh")
	}
	dir := t.TempDir()
	t.Chdir(dir) // pi's launch makes its session folder here
	prompts := []string{"--dangerously-skip-permissions", "-c x=y", "--sandbox=danger-full-access", "@secrets.txt", "/help"}
	agents := []struct {
		name string
		ag   Agent
	}{{"claude", &ClaudeAgent{}}, {"codex", &CodexAgent{}}, {"agy", &AgyAgent{}}, {"pi", &PiAgent{}}}
	n := 0
	for _, a := range agents {
		for _, prompt := range prompts {
			n++
			argvFile := filepath.Join(dir, fmt.Sprintf("%s-%d.argv", a.name, n))
			p := LaunchParams{
				SessionID:   fmt.Sprintf("11111111-0036-4000-8000-%012d", n),
				SessionName: a.name + "-x", CLIPath: printer, Prompt: prompt, CoralPort: 8420, WorkingDir: dir,
			}
			line := WrapWithBundlePath(buildLaunch(a.ag, p))
			t.Setenv("ARGV_OUT", argvFile)
			runLaunchLine(t, line)
			data, err := os.ReadFile(argvFile)
			require.NoError(t, err, "%s %q", a.name, prompt)
			CleanupTempFiles(p.SessionID)
			var argv []string
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				u, err := strconv.Unquote(strings.TrimSpace(l))
				require.NoError(t, err, "%s: %q", a.name, l)
				argv = append(argv, u)
			}
			at := -1
			for i, x := range argv {
				if strings.Contains(x, prompt) {
					at = i
				}
			}
			require.GreaterOrEqual(t, at, 0, "%s %q: the prompt is in the arguments: %q", a.name, prompt, argv)
			assert.True(t, strings.HasSuffix(strings.TrimSpace(argv[at]), prompt), "%s: the prompt arrives whole, as the end of one argument: %q", a.name, argv)
			switch a.name {
			case "claude", "codex":
				if a.name == "claude" && strings.HasPrefix(prompt, "/") {
					assert.True(t, strings.HasPrefix(argv[at], "\n/"), "claude: a prompt starting with / has a newline in front, so it isn't a slash command: %q", argv[at])
				}
				require.Greater(t, at, 0)
				assert.Equal(t, "--", argv[at-1], "%s %q: after the end-of-options marker: %q", a.name, prompt, argv)
			case "agy":
				require.Greater(t, at, 0)
				assert.Equal(t, "-i", argv[at-1], "%s %q: the value of -i: %q", a.name, prompt, argv)
			default:
				assert.False(t, strings.HasPrefix(argv[at], "-") || strings.HasPrefix(argv[at], "@"), "%s %q: starts like an option or a file: %q", a.name, prompt, argv)
			}
		}
	}
}
