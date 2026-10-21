package ptymanager

import (
	"strings"
	"testing"
	"time"
)

// pollWindow is how long a test waits for something a started process or pane is to do. A test that runs beside others (t.Parallel) or on a small
// runner is slow to start its process, so the wait is for the output, not for a guessed time (#5671).
const pollWindow = 15 * time.Second

// pollFor returns what get returns as soon as it contains want, or what it returned last when pollWindow is over (the caller's own assertion then
// reports it).
func pollFor(get func() string, want string) string {
	deadline := time.Now().Add(pollWindow)
	for {
		got := get()
		if strings.Contains(got, want) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// eventually fails the test when cond is still false after pollWindow (waitFor's 5 s is the window for the gate tests).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollWindow)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
