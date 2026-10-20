//go:build !windows

package background

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startStep(t *testing.T, script string) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	configureChildProcess(cmd)
	require.NoError(t, cmd.Start())
	reaped := make(chan struct{})
	go func() { cmd.Wait(); close(reaped) }() // the runner reaps its step as well
	t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd, reaped
}

// #5366: the escalation to SIGKILL ends as soon as the group is gone (a step that obeys SIGTERM does not keep a goroutine 5 s), and waitChildKills
// returns then.
func TestKillChildProcess_AStepThatEndsOnSIGTERMEndsTheEscalationAtOnce(t *testing.T) {
	cmd, reaped := startStep(t, "sleep 60")
	start := time.Now()
	killChildProcess(cmd)
	<-reaped
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waitChildKills(ctx)
	assert.Less(t, time.Since(start), 2*time.Second, "the escalation did not sleep out its 5 s grace")
	assert.NoError(t, ctx.Err())
}

// #5366: a step that ignores SIGTERM is killed before waitChildKills returns, so it cannot outlive a Coral that exits right after its shutdown.
func TestKillChildProcess_AStepThatIgnoresSIGTERMIsKilledBeforeShutdownReturns(t *testing.T) {
	old := childKillGrace
	childKillGrace = 300 * time.Millisecond
	t.Cleanup(func() { childKillGrace = old })
	cmd, reaped := startStep(t, `trap '' TERM; while :; do sleep 1; done`)
	time.Sleep(200 * time.Millisecond) // the trap is set
	start := time.Now()
	killChildProcess(cmd)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitChildKills(ctx)
	require.NoError(t, ctx.Err(), "waitChildKills returned on its own")
	assert.GreaterOrEqual(t, time.Since(start), childKillGrace, "it waited for the SIGKILL")
	select {
	case <-reaped:
	case <-time.After(2 * time.Second):
		t.Fatal("the step ignoring SIGTERM was still running after waitChildKills returned")
	}
	assert.Error(t, syscall.Kill(-cmd.Process.Pid, 0), "the process group is gone")
}
