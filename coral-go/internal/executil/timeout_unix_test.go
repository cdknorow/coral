//go:build !windows

package executil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3999 (finding 4): a helper that never returns is killed at the deadline.
func TestOutputAndRunKillAHungCommand(t *testing.T) {
	start := time.Now()
	_, err := Output(200*time.Millisecond, "sleep", "30")
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "Output returned at the deadline, not after 30 s")

	start = time.Now()
	assert.Error(t, Run(200*time.Millisecond, "sleep", "30"))
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestOutputReturnsWhatACommandPrints(t *testing.T) {
	out, err := Output(5*time.Second, "echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}
