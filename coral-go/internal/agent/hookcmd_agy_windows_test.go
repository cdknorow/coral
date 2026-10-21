//go:build windows

package agent

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
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

			cmd, ok := ResolveCmdHookCommand("coral-hook-agentic-state --agy PreToolUse")
			assert.NotContains(t, cmd, `"`, "cmd.exe can't read Go's escaped quotes")
			assert.True(t, strings.HasSuffix(cmd, " --agy PreToolUse"), cmd)
			// The quoted form fails exactly as the operator saw.
			out, err := runAsAgy(work, ResolveHookCommand("coral-hook-agentic-state --agy PreToolUse"))
			require.Error(t, err, "%s", out)
			assert.Contains(t, out, "is not recognized")

			long := filepath.ToSlash(real)
			switch {
			case cmdWord(long):
				assert.True(t, ok)
				assert.Equal(t, long+" --agy PreToolUse", cmd)
			case shortPathName(tools) == "" || !cmdWord(filepath.ToSlash(shortPathName(tools))):
				// A volume without 8.3 names: no command at all (#21), never a bare name or the
				// quoted path.
				assert.False(t, ok)
				assert.Empty(t, cmd)
				t.Skip("no 8.3 short names on this volume")
			default:
				want := filepath.ToSlash(shortPathName(tools)) + "/coral-hook-agentic-state.exe --agy PreToolUse"
				assert.True(t, ok)
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

// #21 (Security's #20 F1): on Windows, with no absolute path cmd.exe can run (no tools folder,
// no tool in it, or a folder cmdWord refuses with no 8.3 name), no hook command is made and an
// agy launch installs no hooks: never a bare name, which cmd.exe would find in the agent's
// repository first. The launch line also turns off cmd.exe's current-folder search.
func TestAgyHooksFailClosedOnWindows(t *testing.T) {
	t.Setenv(HookDirEnv, t.TempDir()) // a tools folder without the hook
	cmd, ok := ResolveCmdHookCommand("coral-hook-agentic-state --agy Stop")
	assert.False(t, ok)
	assert.Empty(t, cmd)
	t.Setenv(HookDirEnv, "")
	SetInstalledToolsFallback(false)
	t.Cleanup(func() { SetInstalledToolsFallback(true) })
	if CoralToolsDir() == "" {
		_, ok = ResolveCmdHookCommand("coral-hook-agentic-state --agy Stop")
		assert.False(t, ok, "no tools folder")
	}
	// A folder cmd.exe can't read as one word, here a UNC path: no command.
	t.Setenv(HookDirEnv, `\localhost\C$\no-such-coral`)
	_, ok = ResolveCmdHookCommand("coral-hook-agentic-state --agy Stop")
	assert.False(t, ok)

	t.Setenv(HookDirEnv, t.TempDir())
	sid := "a9f3c4d1-0000-4000-8000-000000000021"
	var logged strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	line := buildLaunch(&AgyAgent{}, LaunchParams{SessionID: sid, WorkingDir: t.TempDir()})
	assert.NotContains(t, line, "--add-dir", "no hooks folder")
	assert.NotContains(t, line, "coral-hook-agentic-state")
	assert.Contains(t, line, "$env:NoDefaultCurrentDirectoryInExePath='1';")
	assert.Equal(t, 1, strings.Count(logged.String(), "agy runs without Coral's hooks"), "one warning per launch: %s", logged.String())
	assert.Contains(t, logged.String(), sid)
	dirs, _ := filepath.Glob(filepath.Join(LaunchTempDir(), "coral_agyhooks_"+sid+"_*"))
	assert.Empty(t, dirs, "nothing written")
}

// #21 (#25 H2): the hooks folder and hooks.json, the commands agy runs, are private: this user
// and SYSTEM only, nothing inherited.
func TestAgyHooksFolderIsPrivate(t *testing.T) {
	tools := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tools, "coral-hook-agentic-state.exe"), nil, 0o700))
	t.Setenv(HookDirEnv, tools)
	dir, err := agyHooksDir("a9f3c4d1-0000-4000-8000-000000000025")
	require.NoError(t, err)
	require.NotEmpty(t, dir)
	defer os.RemoveAll(dir)
	for _, p := range []string{dir, filepath.Join(dir, ".agents"), filepath.Join(dir, ".agents", "hooks.json")} {
		secfiletest.RequirePrivate(t, p)
	}
}

// #49: Codex runs its hooks through PowerShell on Windows (0.161), where a quoted path is a string
// PowerShell prints, so the hook never ran. ResolvePowerShellHookCommand calls the tool by its
// absolute path: run here as Codex runs it, in folders with a space and (x86), $ and an
// apostrophe, from the decoy's folder, it runs the tools folder's copy. The old quoted form is
// shown printing the path.
func TestPowerShellHookCommandRunsUnderCodexsPowerShell(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no Windows PowerShell")
	}
	for _, folder := range []string{`Program Files (x86)\Coral`, `Coral`, `O'Brien $HOME`} {
		t.Run(folder, func(t *testing.T) {
			tools := filepath.Join(t.TempDir(), folder)
			require.NoError(t, os.MkdirAll(tools, 0o700))
			real := filepath.Join(tools, "coral-hook-agentic-state.exe")
			copyTestBinary(t, real)
			work := t.TempDir()
			copyTestBinary(t, filepath.Join(work, "coral-hook-agentic-state.exe"))
			t.Setenv(HookDirEnv, tools)

			cmd, ok := ResolvePowerShellHookCommand("coral-hook-agentic-state")
			require.True(t, ok)
			assert.True(t, strings.HasPrefix(cmd, "& '"), cmd)
			run := func(command string) string {
				c := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", command)
				c.Dir = work
				c.Env = append(os.Environ(), hookEchoEnv+"=1", "PATH="+work+string(os.PathListSeparator)+os.Getenv("PATH"))
				out, err := c.CombinedOutput()
				require.NoError(t, err, "%s", out)
				return strings.TrimSpace(string(out))
			}
			ran, err := os.Stat(run(cmd))
			require.NoError(t, err)
			want, err := os.Stat(real)
			require.NoError(t, err)
			assert.True(t, os.SameFile(want, ran), "ran the tools folder's hook")
			if quoted := ResolveHookCommand("coral-hook-agentic-state"); strings.HasPrefix(quoted, `"`) {
				assert.Equal(t, strings.Trim(quoted, `"`), run(quoted), "the old form: PowerShell prints the path, runs nothing")
			}
		})
	}
	// No tool: no command, never the bare name; a Codex launch then has no hooks, logged once
	// (Security's #49 note).
	t.Setenv(HookDirEnv, t.TempDir())
	cmd, ok := ResolvePowerShellHookCommand("coral-hook-agentic-state")
	assert.False(t, ok)
	assert.Empty(t, cmd)
	var logged strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	sid := "a9f3c4d1-0000-4000-8000-000000000049"
	line := buildLaunch(&CodexAgent{}, LaunchParams{SessionID: sid, WorkingDir: t.TempDir()})
	assert.NotContains(t, line, "hooks.")
	assert.NotContains(t, line, "coral-hook-agentic-state")
	assert.Equal(t, 1, strings.Count(logged.String(), "codex runs without Coral's hooks"), logged.String())
	assert.Contains(t, logged.String(), sid)
	CleanupTempFiles(sid)
}
