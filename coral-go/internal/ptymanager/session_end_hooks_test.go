package ptymanager

import (
	"sync"
	"testing"
)

// #4057: DropHeld (a session's kill, restart, resume or new launch) calls every registered
// session-end hook with the session's name, its tmux target suffix removed.
func TestDropHeldCallsTheSessionEndHooks(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	OnSessionEnd(func(name string) {
		mu.Lock()
		got[name]++
		mu.Unlock()
	})
	DropHeld("claude-4057-hook", "killed")
	DropHeld("claude-4057-hook:0.0", "restarted")
	mu.Lock()
	defer mu.Unlock()
	if got["claude-4057-hook"] != 2 {
		t.Fatalf("hook calls: %v", got)
	}
}
