//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5480 (Security's condition 2): a model id may hold [ ] @ : / + (claude-sonnet-4-5[1m] is a real one), and [ ] are shell glob characters. Through the launch line a real /bin/sh
// runs, the CLI receives the id as ONE literal argument, even with a file in the folder that the pattern would match.
func TestLaunchLineGivesTheCLIAGlobShapedModelIdLiterally(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Chdir(dir)
	got := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "agent-cli")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '"+got+"'; done\nexit 0\n"), 0o755))

	for _, model := range []string{
		"claude-sonnet-4-5[1m]", "m[abc]x", "vendor/model@2:high+x", "a[!x]", "a*", "a?b", strings.Repeat("a", 100) + "[1m]",
	} {
		// Files the glob would match, if the shell saw it unquoted.
		for _, f := range []string{"claude-sonnet-4-5m", "maxx", "mbx", "a", "ab", "aa", "a-literal"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
		}
		os.Remove(got)
		for name, ag := range map[string]Agent{"claude": &ClaudeAgent{}, "codex": &CodexAgent{}, "agy": &AgyAgent{}, "pi": &PiAgent{}} {
			p := LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", SessionName: "claude-x", CLIPath: stub, BoardName: "team", Role: "Dev", Prompt: "hello", CoralPort: 8420,
				Flags: []string{"--model", model}}
			out, _ := exec.Command("/bin/sh", "-c", WrapWithBundlePath(buildLaunch(ag, p))).CombinedOutput()
			args, err := os.ReadFile(got)
			require.NoError(t, err, "%s %q: %s", name, model, out)
			lines := strings.Split(strings.TrimSpace(string(args)), "\n")
			i := -1
			for j, l := range lines {
				if l == "--model" {
					i = j
				}
			}
			require.True(t, i >= 0 && i+1 < len(lines), "%s: %v", name, lines)
			assert.Equal(t, model, lines[i+1], "%s: the id arrives as one literal argument", name)
			os.Remove(got)
			CleanupTempFiles(p.SessionID)
		}
	}
}
