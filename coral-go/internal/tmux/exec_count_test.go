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

// #4077: a capture is also counted under the code that asked for it.
func TestCapturePaneCountedByCaller(t *testing.T) {
	key := "capture-pane<tmux.captureFromTest4077"
	before := ExecCounts()[key]
	captureFromTest4077()
	assert.Equal(t, before, ExecCounts()[key], "frames in the tmux package are skipped")
	got := ExecCounts()
	found := false
	for k := range got {
		if k == "capture-pane<testing.tRunner" {
			found = true
		}
	}
	assert.True(t, found, "the first frame outside tmux names the caller: %v", got)
}

func captureFromTest4077() { countExec("capture-pane") }
