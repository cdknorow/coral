//go:build windows

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

// agy runs each hook as exec.Command("cmd", "/c", command): Go's quoting turns a " into \",
// which cmd.exe doesn't read, so a hook command quoting its path fails. ResolveCmdHookCommand
// names the tool by its absolute path without quotes, and the command runs the tools folder's
// hook, not a decoy of the same name in the agent's folder or first on PATH (#3660).
func TestCmdHookCommandRunsUnderAgysCmd(t *testing.T) {
	for _, tc := range []struct{ name, folder string }{
		{"spaces and (x86)", `Program Files (x86)\Coral`},
		{"no spaces", `Coral`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := filepath.Join(t.TempDir(), tc.folder)
			require.NoError(t, os.MkdirAll(tools, 0o700))
			real := filepath.Join(tools, "coral-hook-agentic-state.exe")
			copyTestBinary(t, real)
			work := t.TempDir()
			copyTestBinary(t, filepath.Join(work, "coral-hook-agentic-state.exe"))
			t.Setenv(HookDirEnv, tools)

			cmd := ResolveCmdHookCommand("coral-hook-agentic-state --agy PreToolUse")
			assert.NotContains(t, cmd, `"`, "cmd.exe can't read Go's escaped quotes")
			assert.True(t, strings.HasSuffix(cmd, " --agy PreToolUse"), cmd)
			// The quoted form fails exactly as the operator saw.
			out, err := runAsAgy(work, ResolveHookCommand("coral-hook-agentic-state --agy PreToolUse"))
			require.Error(t, err, "%s", out)
			assert.Contains(t, out, "is not recognized")

			long := filepath.ToSlash(real)
			switch {
			case cmdWord(long):
				assert.Equal(t, long+" --agy PreToolUse", cmd)
			case shortPathName(tools) == "" || !cmdWord(filepath.ToSlash(shortPathName(tools))):
				// A volume without 8.3 names: the bare name (logged), never the quoted path.
				assert.Equal(t, "coral-hook-agentic-state --agy PreToolUse", cmd)
				t.Skip("no 8.3 short names on this volume")
			default:
				want := filepath.ToSlash(shortPathName(tools)) + "/coral-hook-agentic-state.exe --agy PreToolUse"
				assert.Equal(t, want, cmd)
				assert.Contains(t, cmd, "~", "the folder's short name")
			}

			out, err = runAsAgy(work, cmd)
			require.NoError(t, err, "%s", out)
			ran, err := os.Stat(strings.TrimSpace(out))
			require.NoError(t, err, "the hook printed %q", out)
			want, err := os.Stat(real)
			require.NoError(t, err)
			assert.True(t, os.SameFile(want, ran), "ran %s, not the tools folder's hook", out)
		})
	}
}

// runAsAgy runs a hook command the way agy does, from the agent's folder with that folder first
// on PATH.
func runAsAgy(work, command string) (string, error) {
	c := exec.Command("cmd", "/c", command)
	c.Dir = work
	c.Env = append(os.Environ(), hookEchoEnv+"=1", "PATH="+work+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := c.CombinedOutput()
	return string(out), err
}

func TestCmdWord(t *testing.T) {
	for p, want := range map[string]bool{
		"C:/Coral/coral-hook-agentic-state.exe":                     true,
		"C:/PROGRA~2/Coral/coral-hook-agentic-state.exe":            true,
		"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe": false,
		"C:/a&b/x.exe": false, "C:/a^b/x.exe": false, "C:/%TEMP%/x.exe": false, "C:/a!b/x.exe": false,
		"C:/a(b/x.exe": false, "C:/a;b/x.exe": false, "C:/a,b/x.exe": false, "C:/a=b/x.exe": false,
		"C:/a|b/x.exe": false, "C:/a<b/x.exe": false, `C:/a"b/x.exe`: false, "C:/a\tb/x.exe": false,
		"": false,
	} {
		assert.Equal(t, want, cmdWord(p), p)
	}
}
