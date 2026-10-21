//go:build windows

package ptymanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/winshell"
)

// #25 G3A: a stock Windows client's execution policy is Restricted, which refuses the .ps1 the long-command handoff
// runs. The policy is forced for the test's processes only (PSExecutionPolicyPreference), never on the machine.
// Only the child that runs Coral's script gets -ExecutionPolicy Bypass: the agent's terminal shell keeps the
// machine's policy (Security's review of #26).
func TestLaunchScriptRunsUnderARestrictedExecutionPolicy(t *testing.T) {
	invocation, cleanup, err := commandInvocation("Write-Output coral-handoff-ran")
	require.NoError(t, err)
	t.Cleanup(cleanup)
	env := append(os.Environ(), "PSExecutionPolicyPreference=Restricted")

	sh, err := defaultShell()
	require.NoError(t, err)
	require.Equal(t, 1, len(sh), "the terminal shell is started with no ExecutionPolicy flag: %v", sh)

	run := func(command string) (string, error) {
		cmd := exec.Command(sh[0], "-NoProfile", "-Command", command)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(invocation)
	require.NoError(t, err, out)
	assert.Contains(t, out, "coral-handoff-ran", "the line typed into the Restricted terminal runs the script")
	assert.Contains(t, invocation, "-ExecutionPolicy Bypass -File", "the one child that runs the script has the flag")

	// The terminal shell itself is still Restricted: a script run in it directly is refused.
	script := filepath.Join(t.TempDir(), "other.ps1")
	require.NoError(t, os.WriteFile(script, []byte("Write-Output other-ran"), 0o600))
	out, _ = run("& " + psLiteral(script))
	if strings.Contains(out, "other-ran") {
		t.Log("the machine does not enforce Restricted for this process (the control needs it to)")
	} else {
		assert.Contains(t, out, "disabled", "the terminal shell keeps the machine's policy")
	}
}

// shellWrap (a command with shell syntax) has no policy override either: it runs no script.
func TestShellWrapKeepsTheMachinePolicy(t *testing.T) {
	ps := winshell.PowerShell() // the PowerShell the launch line was written for, by absolute path (#26 G1A)
	if ps == "" {
		ps = "powershell.exe"
	}
	assert.Equal(t, []string{ps, "-NoProfile", "-Command", "x | y"}, shellWrap("x | y"))
}

// Security's N1: -ExecutionPolicy sets PSExecutionPolicyPreference in the handoff child's environment, and every PowerShell the
// agent's tools start would inherit it. The script drops it first: a powershell.exe started from the script reports
// no Process policy.
func TestHandoffBypassDoesNotReachThePowerShellsTheAgentStarts(t *testing.T) {
	ps := powerShellForTest(t)
	grandchild := "& " + psLiteral(ps) + " -NoProfile -Command Get-ExecutionPolicy -Scope Process"
	invocation, cleanup, err := commandInvocation(grandchild)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	sh, err := defaultShell()
	require.NoError(t, err)
	cmd := exec.Command(sh[0], "-NoProfile", "-Command", invocation)
	cmd.Env = append(os.Environ(), "PSExecutionPolicyPreference=Restricted")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "Undefined", "a PowerShell the agent starts has no Process policy of its own")
	assert.NotContains(t, string(out), "Bypass")
	assert.NotContains(t, string(out), "Restricted")
}
