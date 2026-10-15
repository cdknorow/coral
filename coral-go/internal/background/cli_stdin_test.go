package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3519: the prompts Coral sends to "claude --print" (an agent's transcript for the
// knowledge distill and the session summary, the session's messages for a goal) go on
// stdin, never in argv, where any local user could read them in the process list.
func TestClaudePromptsGoOnStdinNotArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	dir := t.TempDir()
	argsFile, stdinFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	bin := filepath.Join(dir, "claude-fake")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat > '" + stdinFile + "'\n" +
		"echo '{\"type\":\"result\",\"result\":\"ok\",\"is_error\":false}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	secret := "TRANSCRIPT-SECRET-3519 " + strings.Repeat("x", 2000)

	check := func(what string) {
		t.Helper()
		args, err := os.ReadFile(argsFile)
		require.NoError(t, err, what)
		in, err := os.ReadFile(stdinFile)
		require.NoError(t, err, what)
		assert.NotContains(t, string(args), "TRANSCRIPT-SECRET-3519", "%s: nothing of the prompt in argv", what)
		assert.Contains(t, string(in), "TRANSCRIPT-SECRET-3519", "%s: the prompt arrives on stdin", what)
		os.Remove(argsFile)
		os.Remove(stdinFile)
	}

	_, err := callClaudeForKnowledge(context.Background(), bin, 10*time.Second, "system prompt", secret)
	require.NoError(t, err)
	check("knowledge distill")

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _ = callClaude(context.Background(), map[string]string{"cli_path_claude": bin}, secret)
	check("session summary")

	_, _ = runGoalCLI(context.Background(), bin, secret)
	check("goal")
}
