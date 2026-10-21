package agent

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #36 (Security's W1): a workflow step's prompt (earlier steps' output, board text) that starts like an option
// reaches every CLI as the prompt. The step's real argument list is run against a program that records its
// arguments and its standard input.
func TestAStepPromptThatLooksLikeAnOptionIsThePrompt(t *testing.T) {
	printer := argvPrinter(t)
	dir := t.TempDir()
	prompts := []string{"--dangerously-skip-permissions", "-c x=y", "--sandbox danger-full-access", "@secrets.txt"}
	n := 0
	for _, agentType := range []string{"codex", "agy", "pi", "gemini"} {
		for _, prompt := range prompts {
			n++
			argvFile := filepath.Join(dir, agentType+"-"+strconv.Itoa(n)+".argv")
			args, stdin := StepPrintArgs(agentType, []string{"--flag-from-config"}, []string{"--model", "m1"}, prompt)
			cmd := exec.Command(printer, args...)
			cmd.Env = append(os.Environ(), "ARGV_OUT="+argvFile)
			if stdin != "" {
				cmd.Stdin = strings.NewReader(stdin)
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s: %s", agentType, out)
			data, err := os.ReadFile(argvFile)
			require.NoError(t, err)
			var argv []string
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				u, err := strconv.Unquote(strings.TrimSpace(l))
				require.NoError(t, err)
				argv = append(argv, u)
			}
			label := agentType + " " + prompt
			switch agentType {
			case "codex":
				assert.Equal(t, "-", argv[len(argv)-1], label)
				assert.Equal(t, "exec", argv[0], label)
				got, err := os.ReadFile(argvFile + ".stdin")
				require.NoError(t, err, label)
				assert.True(t, bytes.Equal([]byte(prompt), got), "%s: the prompt is on stdin: %q", label, got)
				for _, a := range argv {
					assert.NotEqual(t, prompt, a, "%s: the prompt is not an argument", label)
				}
			case "agy":
				require.GreaterOrEqual(t, len(argv), 2, label)
				assert.Equal(t, []string{"--disable-slash-commands", "--print", prompt}, argv[len(argv)-3:], "%s: slash commands off, then --print and its value last", label)
				assert.NoFileExists(t, argvFile+".stdin")
			default:
				last := argv[len(argv)-1]
				assert.Equal(t, strings.TrimLeft(prompt, "\n"), strings.TrimLeft(last, "\n"), label)
				assert.False(t, strings.HasPrefix(last, "-") || strings.HasPrefix(last, "@"), "%s: starts like an option or a file: %q", label, last)
			}
			// The Claude-only flag is not passed to the others.
			assert.NotContains(t, argv, "--no-session-persistence", label)
		}
	}
}
