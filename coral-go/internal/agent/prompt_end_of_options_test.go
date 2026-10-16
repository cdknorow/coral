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

// #3690 (#3689 finding 4): a prompt that is only a flag reaches Claude and Codex after "--", so
// their parsers (commander, clap) take it as the prompt, not as that option. The launch line is
// really run, by /bin/sh, against a stub that records its arguments.
func TestAFlagLikePromptComesAfterEndOfOptions(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	for name, ag := range map[string]Agent{"claude": &ClaudeAgent{}, "codex": &CodexAgent{}} {
		argvFile := filepath.Join(dir, name+".argv")
		stub := filepath.Join(dir, name+"-cli")
		require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '"+argvFile+"'\n"), 0o755))
		p := LaunchParams{SessionID: "11111111-3690-4000-8000-00000000000" + map[string]string{"claude": "1", "codex": "2"}[name],
			SessionName: name + "-x", CLIPath: stub, Prompt: "--dangerously-skip-permissions", CoralPort: 8420}
		cmd := WrapWithBundlePath(buildLaunch(ag, p))
		out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
		require.NoError(t, err, "%s\n%s", cmd, out)
		data, err := os.ReadFile(argvFile)
		require.NoError(t, err)
		argv := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		require.GreaterOrEqual(t, len(argv), 2, name)
		assert.Equal(t, "--", argv[len(argv)-2], "%s: %q", name, argv)
		assert.Contains(t, argv[len(argv)-1], "--dangerously-skip-permissions", name)
		n := 0
		for _, a := range argv {
			if a == "--" {
				n++
			}
		}
		assert.Equal(t, 1, n, "%s: one end-of-options marker: %q", name, argv)
		CleanupTempFiles(p.SessionID)
	}
}
