package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
)

// #5661: a workflow step sends Claude its prompt on stdin, and print mode runs a prompt that starts with "/" as a slash command or skill. Step prompts carry earlier steps' output and board
// text, so a leading "/" gets a newline in front (as the interactive launch does): the text is the same, and it is a prompt. Any other prompt arrives byte for byte as before.
func TestAClaudeStepPromptThatStartsWithASlashIsNotACommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	db, path := fakeClaude(t)
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 9.9.9; exit 0; fi\ncat > '"+stdinFile+"'\n"), 0o755))
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	for prompt, want := range map[string]string{
		"/compact":               "\n/compact",
		"/help me with this":     "\n/help me with this",
		"summarize /compact":     "summarize /compact",
		" /compact":              " /compact",
		"\n/compact":             "\n/compact",
		"say hi":                 "say hi",
		"/":                      "\n/",
		"//etc/passwd is a path": "\n//etc/passwd is a path",
	} {
		require.NoError(t, runner.executeAgentPrint(context.Background(), 1, StepDef{Name: "s"}, testStepFiles(t), t.TempDir(), os.Environ(), &AgentStepConfig{AgentType: at.Claude}, prompt, nil, &StepResult{}))
		in, err := os.ReadFile(stdinFile)
		require.NoError(t, err)
		assert.Equal(t, want, string(in), "prompt %q", prompt)
		os.Remove(stdinFile)
	}
}
