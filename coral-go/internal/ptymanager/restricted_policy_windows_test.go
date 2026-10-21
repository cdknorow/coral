//go:build windows

package ptymanager

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #25 G3A: a stock Windows client's execution policy is Restricted, which refuses the .ps1 the long-command handoff
// runs. The policy is forced for the test's processes only (PSExecutionPolicyPreference), never on the machine.
func TestLaunchScriptRunsUnderARestrictedExecutionPolicy(t *testing.T) {
	invocation, cleanup, err := commandInvocation("Write-Output coral-handoff-ran")
	require.NoError(t, err)
	t.Cleanup(cleanup)
	env := append(os.Environ(), "PSExecutionPolicyPreference=Restricted")

	run := func(shell []string) (string, error) {
		args := append(append([]string{}, shell[1:]...), "-NoProfile", "-Command", invocation)
		cmd := exec.Command(shell[0], args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	sh, err := defaultShell()
	require.NoError(t, err)
	out, err := run(sh)
	require.NoError(t, err, out)
	assert.Contains(t, out, "coral-handoff-ran", "the shell Coral starts runs the handoff script under Restricted")

	// The same shell without the flag: the repro.
	if out, err := run(sh[:1]); err == nil && strings.Contains(out, "coral-handoff-ran") {
		t.Log("the machine does not enforce Restricted for this process (the repro needs it to)")
	}
}

// shellWrap (a command with shell syntax) carries the same flag.
func TestShellWrapCarriesTheProcessExecutionPolicy(t *testing.T) {
	assert.Equal(t, []string{"powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "x | y"}, shellWrap("x | y"))
}
