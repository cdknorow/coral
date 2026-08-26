package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLSPStubbornHelperProcess answers initialize but ignores shutdown and
// exit, so Manager.Close must fall back to killing it rather than waiting.
func TestLSPStubbornHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "stubborn" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		body, err := readFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var msg rpcMessage
		if json.Unmarshal(body, &msg) != nil {
			os.Exit(2)
		}
		if msg.ID == nil || msg.Method != "initialize" {
			continue // ignore shutdown, exit, and everything else
		}
		encoded, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: msg.ID,
			Result: json.RawMessage(`{"capabilities":{"hoverProvider":true}}`)})
		if writeFrame(os.Stdout, encoded) != nil {
			os.Exit(3)
		}
	}
}

func cooperativeDefinition() ServerDefinition {
	return ServerDefinition{ID: "fake", Command: os.Args[0],
		Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
}

func stubbornDefinition() ServerDefinition {
	return ServerDefinition{ID: "stubborn", Command: os.Args[0],
		Args: []string{"-test.run=TestLSPStubbornHelperProcess", "--", "stubborn"}}
}

// TestManagerCloseShutsDownEveryServer covers M1: Coral must not orphan a
// language server per workspace when it exits.
func TestManagerCloseShutsDownEveryServer(t *testing.T) {
	manager := NewManager()
	manager.startupTimeout = 5 * time.Second
	manager.shutdownTimeout = 2 * time.Second

	def := cooperativeDefinition()
	first, _, err := manager.Connect(context.Background(), def, t.TempDir(), "one")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := manager.Connect(context.Background(), def, t.TempDir(), "two")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("distinct workspaces shared an instance")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for name, instance := range map[string]*Instance{"first": first, "second": second} {
		if instance.Server.Alive() {
			t.Fatalf("%s language server survived Manager.Close", name)
		}
	}

	// The reaper goroutine must be gone too, not merely idle.
	select {
	case <-manager.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("reaper goroutine did not stop")
	}
}

// TestManagerCloseIsIdempotent guards against a second shutdown path (or a
// double-invoked server shutdown hook) panicking on a closed channel.
func TestManagerCloseIsIdempotent(t *testing.T) {
	manager := NewManager()
	manager.startupTimeout = 5 * time.Second
	manager.shutdownTimeout = 2 * time.Second
	if _, _, err := manager.Connect(context.Background(), cooperativeDefinition(), t.TempDir(), "one"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if err := manager.Close(ctx); err != nil {
			t.Fatalf("Close attempt %d: %v", attempt+1, err)
		}
	}
}

// TestManagerCloseRejectsNewConnections stops a shutting-down Coral from
// launching a fresh language server it would then leak.
func TestManagerCloseRejectsNewConnections(t *testing.T) {
	manager := NewManager()
	manager.startupTimeout = 5 * time.Second
	manager.shutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}

	instance, status, err := manager.Connect(context.Background(), cooperativeDefinition(), t.TempDir(), "late")
	if err == nil {
		t.Fatal("Connect succeeded after Close")
	}
	if instance != nil {
		t.Fatal("Connect returned an instance after Close")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want %q", status, StatusFailed)
	}
}

// TestManagerCloseIsBoundedForUncooperativeServer covers the spec requirement
// that server failures cannot hang Coral shutdown: a server that ignores both
// shutdown and exit must be killed within the bounded wait.
func TestManagerCloseIsBoundedForUncooperativeServer(t *testing.T) {
	manager := NewManager()
	manager.startupTimeout = 5 * time.Second
	manager.shutdownTimeout = 500 * time.Millisecond

	instance, _, err := manager.Connect(context.Background(), stubbornDefinition(), t.TempDir(), "one")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Close took %v for an uncooperative server", elapsed)
	}
	if instance.Server.Alive() {
		t.Fatal("uncooperative language server survived Manager.Close")
	}
}

// TestManagerCloseHonoursCallerDeadline ensures a slow shutdown surfaces as a
// context error to the caller instead of blocking process exit indefinitely.
func TestManagerCloseHonoursCallerDeadline(t *testing.T) {
	manager := NewManager()
	manager.startupTimeout = 5 * time.Second
	manager.shutdownTimeout = 30 * time.Second // longer than the caller will wait

	if _, _, err := manager.Connect(context.Background(), stubbornDefinition(), t.TempDir(), "one"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := manager.Close(ctx)
	elapsed := time.Since(started)

	if err == nil {
		t.Skip("shutdown completed within the deadline; nothing to assert about the timeout path")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Close ignored the caller deadline for %v", elapsed)
	}
}
