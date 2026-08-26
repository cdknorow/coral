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

func TestFakeServerLifecycle(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, err := Start(ctx, def, t.TempDir(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !server.Capabilities()["hover"] || !server.Capabilities()["definition"] || !server.Capabilities()["references"] {
		t.Fatalf("capabilities: %#v", server.Capabilities())
	}
	for _, method := range []string{"textDocument/hover", "textDocument/definition", "textDocument/references"} {
		result, err := server.Request(ctx, method, map[string]any{})
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var got map[string]any
		if json.Unmarshal(result, &got) != nil || got["method"] != method {
			t.Fatalf("%s result: %s", method, result)
		}
	}
	server.Shutdown(time.Second)
}

func TestCancellationUsesGeneratedServerID(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	server, err := Start(context.Background(), def, t.TempDir(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	registered := make(chan int64, 1)
	done := make(chan pendingResponse, 1)
	go func() {
		result, requestErr := server.RequestMapped(ctx, "slow", nil, func(id int64) { registered <- id })
		done <- pendingResponse{result: result, err: requestErr}
	}()
	var mapped int64
	select {
	case mapped = <-registered:
	case <-time.After(time.Second):
		t.Fatal("request id was not registered")
	}
	server.Cancel(mapped)
	response := <-done
	if response.err != nil {
		t.Fatal(response.err)
	}
	var result struct {
		Cancelled int64 `json:"cancelled"`
	}
	if json.Unmarshal(response.result, &result) != nil || result.Cancelled != mapped {
		t.Fatalf("cancellation used %d, want %d", result.Cancelled, mapped)
	}
}

func TestStartupFailure(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--", "fail"}}
	_, err := Start(context.Background(), def, t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("expected initialize failure")
	}
}

func TestStartupTimeoutIsBounded(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--", "hang"}}
	started := time.Now()
	_, err := Start(context.Background(), def, t.TempDir(), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected startup timeout")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("startup timeout took %v", elapsed)
	}
}

func TestMalformedResponseFailsPendingRequest(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	server, err := Start(context.Background(), def, t.TempDir(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := server.Request(ctx, "malformed", nil); err == nil {
		t.Fatal("malformed response did not fail request")
	}
}

func TestManagerRestartsCrashedServer(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	manager := NewManager()
	manager.startupTimeout = 2 * time.Second
	root := t.TempDir()
	first, _, err := manager.Connect(context.Background(), def, root, "one")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := first.Server.Request(ctx, "crash", nil); err == nil {
		t.Fatal("expected crash error")
	}
	second, _, err := manager.Connect(context.Background(), def, root, "two")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Server.Kill()
	if first == second {
		t.Fatal("crashed instance was reused")
	}
	manager.Disconnect(first, "one") // must not alter replacement client accounting.
	manager.mu.Lock()
	clients := manager.servers[key(second.Workspace, def.ID)].clients
	manager.mu.Unlock()
	if clients != 1 {
		t.Fatalf("replacement clients = %d, want 1", clients)
	}
}

func TestBoundedStderr(t *testing.T) {
	var buffer boundedBuffer
	input := make([]byte, 100<<10)
	if _, err := buffer.Write(input); err != nil {
		t.Fatal(err)
	}
	if got := len(buffer.String()); got != 64<<10 {
		t.Fatalf("stderr retained %d bytes", got)
	}
}

func TestLSPHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "--" && os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	fail := mode == "fail"
	hang := mode == "hang"
	reader := bufio.NewReader(os.Stdin)
	var slowID *json.RawMessage
	for {
		body, err := readFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var msg rpcMessage
		if json.Unmarshal(body, &msg) != nil {
			os.Exit(2)
		}
		if msg.Method == "exit" {
			os.Exit(0)
		}
		if msg.ID == nil {
			if msg.Method == "$/cancelRequest" && slowID != nil {
				var params struct {
					ID int64 `json:"id"`
				}
				_ = json.Unmarshal(msg.Params, &params)
				result, _ := json.Marshal(map[string]any{"cancelled": params.ID})
				encoded, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: slowID, Result: result})
				_ = writeFrame(os.Stdout, encoded)
				slowID = nil
			}
			continue
		}
		if msg.Method == "crash" {
			os.Exit(4)
		}
		if msg.Method == "malformed" {
			_ = writeFrame(os.Stdout, []byte(`{bad json`))
			continue
		}
		if msg.Method == "slow" {
			slowID = msg.ID
			continue
		}
		response := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
		if msg.Method == "initialize" {
			if hang {
				continue
			}
			if fail {
				response.Error = &rpcError{Code: -32000, Message: "controlled failure"}
			} else {
				response.Result = json.RawMessage(`{"capabilities":{"hoverProvider":true,"definitionProvider":true,"referencesProvider":true}}`)
			}
		} else if msg.Method == "shutdown" {
			response.Result = json.RawMessage(`null`)
		} else {
			response.Result, _ = json.Marshal(map[string]any{"method": msg.Method})
		}
		encoded, _ := json.Marshal(response)
		if writeFrame(os.Stdout, encoded) != nil {
			os.Exit(3)
		}
	}
}

func TestDocumentLeaseConflictAndCleanup(t *testing.T) {
	root := t.TempDir()
	file := root + string(os.PathSeparator) + "x.go"
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	uri, _ := PathToURI(file)
	instance := &Instance{Workspace: root, Definition: ServerDefinition{ID: "fake"}}
	manager := &Manager{servers: map[string]*managed{
		key(root, "fake"): {instance: instance, clients: 2, leases: map[string]string{}},
	}}
	if err := manager.Lease(instance, uri, "one"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Lease(instance, uri, "two"); err == nil {
		t.Fatal("expected conflict")
	}
	manager.Release(instance, uri, "one")
	if err := manager.Lease(instance, uri, "two"); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentLeaseAliasReleaseAndDifferentFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one.go", "two.go"} {
		if err := os.WriteFile(root+string(os.PathSeparator)+name, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := PathToURI(root + string(os.PathSeparator) + "one.go")
	two, _ := PathToURI(root + string(os.PathSeparator) + "two.go")
	oneAlias := strings.Replace(one, "file://", "file://localhost", 1)
	instance := &Instance{Workspace: root, Definition: ServerDefinition{ID: "fake"}}
	manager := &Manager{servers: map[string]*managed{
		key(root, "fake"): {instance: instance, clients: 2, leases: map[string]string{}},
	}}
	if err := manager.Lease(instance, one, "one"); err != nil {
		t.Fatal(err)
	}
	if !manager.Owns(instance, oneAlias, "one") {
		t.Fatal("owner was not recognized through URI alias")
	}
	if err := manager.Lease(instance, two, "two"); err != nil {
		t.Fatalf("different file conflicted: %v", err)
	}
	manager.Release(instance, oneAlias, "one")
	if err := manager.Lease(instance, one, "two"); err != nil {
		t.Fatalf("alias release did not free canonical identity: %v", err)
	}
}

func TestManagerSharesWorkspaceAndIsolatesWorktrees(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	manager := NewManager()
	manager.startupTimeout = 2 * time.Second
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, _, err := manager.Connect(ctx, def, firstRoot, "one")
	if err != nil {
		t.Fatal(err)
	}
	shared, _, err := manager.Connect(ctx, def, firstRoot, "two")
	if err != nil {
		t.Fatal(err)
	}
	isolated, _, err := manager.Connect(ctx, def, secondRoot, "three")
	if err != nil {
		t.Fatal(err)
	}
	if first != shared {
		t.Fatal("same canonical workspace did not share an instance")
	}
	if first == isolated {
		t.Fatal("different worktrees shared an instance")
	}
	manager.Disconnect(first, "one")
	manager.Disconnect(shared, "two")
	manager.Disconnect(isolated, "three")
	first.Server.Shutdown(time.Second)
	isolated.Server.Shutdown(time.Second)
}

func TestManagerCloseStopsServersAndIsIdempotent(t *testing.T) {
	def := ServerDefinition{ID: "fake", Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess", "--"}}
	manager := NewManager()
	manager.startupTimeout = time.Second
	manager.shutdownTimeout = 100 * time.Millisecond
	instance, _, err := manager.Connect(context.Background(), def, t.TempDir(), "one")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if instance.Server.Alive() {
		t.Fatal("server remained alive after manager close")
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, status, err := manager.Connect(ctx, def, instance.Workspace, "two"); err == nil || status != StatusFailed {
		t.Fatal("closed manager accepted a connection")
	}
}
