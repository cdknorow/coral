package machinehealth

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fallback for kernels without /proc/<pid>/task/<tid>/children reads each process's parent
// from its stat, and agrees with the children files where both exist.
func TestProcParentsAgreesWithChildren(t *testing.T) {
	c := exec.Command("sleep", "30")
	require.NoError(t, c.Start())
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	parents, err := procParents()
	require.NoError(t, err)
	assert.Equal(t, int32(os.Getpid()), parents[int32(c.Process.Pid)])
	assert.Equal(t, int32(os.Getppid()), parents[int32(os.Getpid())])
	if _, err := os.Stat("/proc/self/task"); err == nil {
		assert.Contains(t, procChildren(int32(os.Getpid())), int32(c.Process.Pid))
	}
}
