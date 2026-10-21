//go:build windows

package ptymanager

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/psquote"
	"github.com/subgentic/coral/internal/winshell"
)

// #12: a Codex launch on Windows PowerShell 5.1, with Coral installed under a folder like
// C:\Program Files (x86)\Coral. The launch line goes through the handoff script, as a long
// one does, and Codex (here a copy of this test binary that prints its arguments) gets each
// hook's -c value exactly as Coral's TOML has it, and the prompt as its file holds it.
func TestCodexLaunchScriptPassesItsArgumentsThroughWindowsPowerShell(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no Windows PowerShell")
	}
	t.Setenv("CORAL_SHELL", ps)
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("TMPDIR", tmp)

	tools := filepath.Join(t.TempDir(), "Program Files (x86)", "Coral")
	require.NoError(t, os.MkdirAll(tools, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tools, "coral-hook-agentic-state.exe"), nil, 0o700))
	t.Setenv(agent.HookDirEnv, tools)
	codex := filepath.Join(tools, "codex.exe")
	copyFile(t, os.Args[0], codex)

	line, err := (&agent.CodexAgent{}).LaunchCommand(agent.LaunchParams{
		SessionID:   "a9f3c4d1-0000-4000-8000-000000000012",
		SessionName: "codex-12",
		CLIPath:     codex,
		BoardName:   "équipe 🎉",
		Role:        "Developer",
		Prompt:      `say "hi" to C:\dir\ and "a b\" — héllo wörld 日本語 🎉`,
		Flags:       []string{"-c", `model="gpt-5"`},
	})
	require.NoError(t, err)
	prompt := regexp.MustCompile(`[^']*coral_codex_prompt_[^']*`).FindString(line)
	require.NotEmpty(t, prompt, line)
	wantPrompt, err := os.ReadFile(prompt)
	require.NoError(t, err)

	inv, cleanup, err := commandInvocation(line)
	require.NoError(t, err)
	defer cleanup()
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", inv)
	cmd.Env = append(os.Environ(), argvEchoEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var args []string
	for _, m := range regexp.MustCompile(`(?s)ARG\[(.*?)\]END\r?\n`).FindAllStringSubmatch(string(out), -1) {
		args = append(args, m[1])
	}
	require.NotEmpty(t, args, "%s", out)

	hook := `"\"` + filepath.ToSlash(filepath.Join(tools, "coral-hook-agentic-state.exe")) + `\""`
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		assert.Contains(t, args, `hooks.`+event+`=[{hooks=[{type="command",command=`+hook+`}]}]`, "%s: %q", event, args)
	}
	assert.Contains(t, args, `model="gpt-5"`, "a user's -c value")
	// The file holds the prompt escaped for 5.1 (#29): what arrived is exactly what it encodes.
	assert.Equal(t, string(wantPrompt), psquote.NativeArg(args[len(args)-1]), "the prompt, byte for byte (audit F6: 5.1 read it as Windows-1252)")
	assert.True(t, strings.HasPrefix(args[len(args)-1], `say "hi" to C:\dir\ and "a b\" — héllo wörld 日本語 🎉`), args[len(args)-1])
	assert.Contains(t, args[len(args)-1], "héllo wörld 日本語 🎉")
	var instructions string
	for _, a := range args {
		if strings.HasPrefix(a, "developer_instructions=") {
			instructions = a
		}
	}
	assert.Contains(t, instructions, `"équipe 🎉"`, "Codex's developer_instructions, read from their file")
	assert.Equal(t, "--", args[len(args)-2])
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(dst, src)
	require.NoError(t, err)
	require.NoError(t, dst.Close())
}

// #29 (Security's #20 F3): a launch command is wrapped in the PowerShell its line was written
// for: shellWrap and the agent package's quoting both take winshell.PowerShell, which follows
// CORAL_SHELL (a pwsh named there is never paired with a line escaped for 5.1, or the reverse).
func TestShellWrapRunsThePowerShellTheLineIsWrittenFor(t *testing.T) {
	for _, shell := range []string{`C:\PS7\pwsh.exe`, "powershell.exe"} {
		t.Setenv("CORAL_SHELL", shell)
		assert.Equal(t, shell, shellWrap("x")[0])
		assert.Equal(t, shell, winshell.PowerShell())
	}
	t.Setenv("CORAL_SHELL", "powershell.exe")
	assert.Equal(t, `'"a""b c"'`, agent.ShellJoin([]string{"x", `a"b c`})[2:], "5.1: escaped")
	t.Setenv("CORAL_SHELL", `C:\PS7\pwsh.exe`)
	assert.Equal(t, `'a"b c'`, agent.ShellJoin([]string{"x", `a"b c`})[2:], "pwsh: not escaped")
}
