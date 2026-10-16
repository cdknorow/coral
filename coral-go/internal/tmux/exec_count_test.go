package tmux

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4049: every tmux command is counted by name, concurrently safe, for execs per minute.
func TestExecCounts(t *testing.T) {
	before := ExecCounts()["test-cmd-4049"]
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); countExec("test-cmd-4049") }()
	}
	wg.Wait()
	assert.Equal(t, before+50, ExecCounts()["test-cmd-4049"])
}
