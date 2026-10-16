package ptymanager

import (
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/safego"
)

// #3998 (Security's #3994 finding 2): with the pty backend every agent runs inside Coral's
// process, so a panic in any goroutine would end them all. A goroutine that panics is recovered
// and the running agent is still there (an unrecovered panic would end this test binary).
func TestPTYAgentsSurviveAGoroutinePanic(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	if err := backend.Spawn("survivor", "test", t.TempDir(), "sid-survivor", "sh -c 'echo ready; sleep 60'", 80, 24); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !backend.IsRunning("survivor") {
		t.Fatal("the agent isn't running before the panic")
	}
	before := safego.Panics()
	safego.Go("test: a request's background work", func() { panic("a bug in a handler's goroutine") })
	for end := time.Now().Add(5 * time.Second); safego.Panics() == before && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if safego.Panics() == before {
		t.Fatal("the panic was not recovered")
	}
	if !backend.IsRunning("survivor") {
		t.Error("the pty agent did not survive the panic")
	}
}
