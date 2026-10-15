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

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
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

// #3521: a workflow agent step sends Claude its prompt on stdin, with or without tools; with
// tools the settings arrive as a real --settings file, not a split shell line ("$(cat",
// env-var names) as before.
func TestWorkflowClaudeStepPromptOnStdin(t *testing.T) {
	db, path := fakeClaude(t)
	dir := t.TempDir()
	argsFile, stdinFile, settingsCopy := filepath.Join(dir, "args"), filepath.Join(dir, "stdin"), filepath.Join(dir, "settings")
	// The fake keeps a copy of the --settings file it was given: Coral removes it afterwards.
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 9.9.9; exit 0; fi\nprintf '%s\\n' \"$@\" > '"+argsFile+"'\n"+
		"prev=; for a in \"$@\"; do if [ \"$prev\" = --settings ]; then cp \"$a\" '"+settingsCopy+"'; fi; prev=$a; done\ncat > '"+stdinFile+"'\n"), 0o755))
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	prompt := "STEP-SECRET-3521 summarize the board"
	for _, cfg := range []*AgentStepConfig{{AgentType: at.Claude}, {AgentType: at.Claude, Tools: []string{"Read"}, Model: "haiku"}} {
		require.NoError(t, runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "s"}, t.TempDir(), t.TempDir(), os.Environ(), cfg, prompt, nil, &StepResult{}))
		args, err := os.ReadFile(argsFile)
		require.NoError(t, err)
		in, err := os.ReadFile(stdinFile)
		require.NoError(t, err)
		a := strings.Split(strings.TrimSpace(string(args)), "\n")
		assert.NotContains(t, string(args), "STEP-SECRET-3521", "tools=%v: the prompt is not in argv", cfg.Tools)
		assert.Contains(t, string(in), "STEP-SECRET-3521", "tools=%v: the prompt is on stdin", cfg.Tools)
		assert.Equal(t, []string{"--print", "--no-session-persistence"}, a[:2])
		for _, bad := range []string{"$(cat", "CORAL_SESSION_NAME", "claude"} {
			assert.NotContains(t, a, bad, "tools=%v: no shell fragments in argv (%v)", cfg.Tools, a)
		}
		if len(cfg.Tools) > 0 {
			i := indexOf(a, "--settings")
			require.True(t, i >= 0 && i+1 < len(a), "a --settings file (%v)", a)
			data, err := os.ReadFile(settingsCopy)
			require.NoError(t, err)
			assert.Contains(t, string(data), `"allowedTools"`, "the tools are in the settings")
			_, err = os.Stat(a[i+1])
			assert.True(t, os.IsNotExist(err), "the step's settings temp file is removed once it has run")
			assert.Equal(t, "haiku", a[indexOf(a, "--model")+1])
		}
		os.Remove(argsFile)
		os.Remove(stdinFile)
	}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}
