package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIHelpEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_HELP_PROBE_SECRET", "must-not-reach-help")
	t.Setenv("NODE_OPTIONS", "--inspect=0")
	require.Equal(t, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, cliHelpEnv())
}

func TestCLIHelpDoesNotPassInheritedCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/probe/bin")
	t.Setenv("CORAL_HELP_PROBE_SECRET", "must-not-reach-help")
	t.Setenv("NODE_OPTIONS", "must-not-reach-help")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-help")
	prog := filepath.Join(t.TempDir(), "help")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PATH\" \"$HOME\" \"${CORAL_HELP_PROBE_SECRET-unset}\" \"${NODE_OPTIONS-unset}\" \"${ANTHROPIC_API_KEY-unset}\"\n"
	require.NoError(t, os.WriteFile(prog, []byte(script), 0o700))
	// This checks inherited credentials, not the production setup deadline.
	assert.Equal(t, os.Getenv("PATH")+"\n"+home+"\nunset\nunset\nunset\n", cliHelpWithTimeout(prog, time.Minute))
}

func TestCLIHelpOutputDrainsPastLimit(t *testing.T) {
	var out cliHelpOutput
	prefix := []byte("Gemini CLI\n")
	n, err := out.Write(prefix)
	require.NoError(t, err)
	require.Equal(t, len(prefix), n)
	large := bytes.Repeat([]byte("x"), 1<<20)
	n, err = out.Write(large)
	require.NoError(t, err)
	assert.Equal(t, len(large), n, "excess output is consumed instead of causing a short write")
	assert.Equal(t, string(prefix)+strings.Repeat("x", cliHelpOutputLimit-len(prefix)), out.String())
	n, err = out.Write([]byte("discarded"))
	require.NoError(t, err)
	assert.Equal(t, len("discarded"), n)
	assert.Len(t, out.String(), cliHelpOutputLimit)
}

func TestCLIHelpCapsCombinedOutputWhileChildFinishes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a POSIX shell script")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	prog := filepath.Join(dir, "help")
	script := "#!/bin/sh\nprintf 'Gemini CLI\\n'\ni=0\nwhile [ \"$i\" -lt 5000 ]; do printf x >&2; i=$((i+1)); done\nprintf done > \"$HOME/finished\"\n"
	require.NoError(t, os.WriteFile(prog, []byte(script), 0o700))
	// This checks output draining and child completion, not probe latency.
	help := cliHelpWithTimeout(prog, time.Minute)
	assert.Len(t, help, cliHelpOutputLimit)
	assert.True(t, strings.HasPrefix(help, "Gemini CLI\n"))
	finished, err := os.ReadFile(filepath.Join(dir, "finished"))
	require.NoError(t, err, "the child can finish after output exceeds the cap")
	assert.Equal(t, "done", string(finished))
}
