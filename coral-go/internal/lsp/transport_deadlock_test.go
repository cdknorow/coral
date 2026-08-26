package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLSPFloodHelperProcess stops reading stdin after initialize and then
// emits a response late, so the parent's large write is already blocked under
// the transport mutex by the time readLoop needs it.
func TestLSPFloodHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "flood" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	body, err := readFrame(reader)
	if err != nil {
		os.Exit(1)
	}
	var msg rpcMessage
	if json.Unmarshal(body, &msg) != nil || msg.Method != "initialize" {
		os.Exit(2)
	}
	encoded, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: msg.ID,
		Result: json.RawMessage(`{"capabilities":{"hoverProvider":true}}`)})
	_ = writeFrame(os.Stdout, encoded)

	// From here on stdin is never read again, so the parent's next large write
	// will fill the pipe and block.
	time.Sleep(2 * time.Second)

	// Answer the request the parent issued after initialize (id 2).
	id := json.RawMessage("2")
	late, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: &id,
		Result: json.RawMessage(`{"answered":true}`)})
	_ = writeFrame(os.Stdout, late)
	time.Sleep(time.Hour)
}

// TestTransportWriteDoesNotBlockResponseDispatch guards the JSON-RPC
// transport against a mutual-blocking cycle.
//
// Server.write holds s.mu for the duration of the write to the child's stdin,
// while readLoop needs that same mutex to dispatch a response. If a write
// blocks because the pipe is full, readLoop can no longer drain stdout, the
// child then blocks on its own write, and it never resumes reading stdin — so
// neither side can make progress and the connection is wedged for good.
//
// This is reachable with ordinary inputs: the POC synchronises whole documents
// on every didChange and permits them up to 5 MiB, while a pipe buffer is
// typically 64 KiB, so any edit to a moderately large file writes far more
// than the pipe can hold.
//
// The fix is to serialize writes on their own mutex so a slow or blocked write
// never stalls response dispatch.
func TestTransportWriteDoesNotBlockResponseDispatch(t *testing.T) {
	def := ServerDefinition{ID: "flood", Command: os.Args[0],
		Args: []string{"-test.run=TestLSPFloodHelperProcess", "--", "flood"}}
	server, err := Start(context.Background(), def, t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Kill)

	// Small request: its write fits in the pipe buffer even though the child
	// has stopped reading, so it is registered and awaits a response.
	requestDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := server.Request(ctx, "textDocument/hover", map[string]any{})
		requestDone <- err
	}()
	time.Sleep(300 * time.Millisecond)

	// Large notification at the documented 5 MiB document limit: this cannot
	// fit in the pipe, so it blocks — while holding s.mu.
	notifyDone := make(chan error, 1)
	go func() {
		notifyDone <- server.Notify("textDocument/didChange", map[string]any{
			"text": strings.Repeat("y", 5<<20),
		})
	}()

	// The child answers at t=2s. A healthy transport delivers that response.
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("transport deadlock: the response was never dispatched because a blocked " +
			"stdin write holds the mutex readLoop needs to deliver it")
	}
	select {
	case err := <-notifyDone:
		t.Logf("notify returned err=%v", err)
	case <-time.After(2 * time.Second):
		t.Logf("notify still blocked on the stdin pipe (acceptable: the child stopped reading)")
	}
}
