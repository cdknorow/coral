package routes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/lsp"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ── Fake language server (helper process) ────────────────────────────
//
// Reproduces the LSP Content-Length transport locally so route tests never
// depend on gopls or on unexported helpers in internal/lsp.

func helperReadFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func helperWriteFrame(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// cancelLogEnv names a file the fake server appends every cancelled subprocess
// request id to. It lets a test observe cancellations the browser never sees —
// notably those issued during disconnect cleanup, after the socket is gone.
const cancelLogEnv = "CORAL_FAKE_LSP_CANCEL_LOG"

// TestLSPRouteHelperProcess is not a real test: it is re-executed as the fake
// language-server subprocess. It exits immediately unless invoked with "--".
//
// A request whose params contain "stall": true is never answered until the
// broker cancels it, which is how tests hold requests in flight.
func TestLSPRouteHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "--" {
		return
	}
	logPath := os.Getenv(cancelLogEnv)
	appendLog := func(kind string, id int64) {
		if logPath == "" {
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "%s %d\n", kind, id)
		f.Close()
	}

	stalled := map[int64]*json.RawMessage{}
	reader := bufio.NewReader(os.Stdin)
	for {
		body, err := helperReadFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params struct {
				ID            int64  `json:"id"`
				Stall         bool   `json:"stall"`
				DefinitionURI string `json:"definitionURI"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil {
			os.Exit(2)
		}
		if msg.Method == "exit" {
			os.Exit(0)
		}
		if msg.ID == nil {
			// Notification. Answer a cancellation by completing the stalled
			// request with the id the broker actually used on the wire.
			if msg.Method == "$/cancelRequest" {
				appendLog("cancel", msg.Params.ID)
				if raw, ok := stalled[msg.Params.ID]; ok {
					delete(stalled, msg.Params.ID)
					result, _ := json.Marshal(map[string]any{"cancelled": msg.Params.ID})
					encoded, _ := json.Marshal(map[string]any{
						"jsonrpc": "2.0", "id": raw, "result": json.RawMessage(result)})
					_ = helperWriteFrame(os.Stdout, encoded)
				}
			}
			continue
		}
		if msg.Params.Stall {
			var id int64
			_ = json.Unmarshal(*msg.ID, &id)
			stalled[id] = msg.ID
			appendLog("stall", id)
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
		switch msg.Method {
		case "initialize":
			appendLog("start", 0)
			response["result"] = json.RawMessage(
				`{"capabilities":{"hoverProvider":true,"definitionProvider":true,"referencesProvider":true}}`)
		case "textDocument/definition":
			// Echo back whichever location the test asked for, so route-level
			// containment checks run against a realistic navigation result.
			uri := msg.Params.DefinitionURI
			if uri == "" {
				cwd, _ := os.Getwd()
				uri = "file://" + filepath.ToSlash(cwd) + "/main.go"
			}
			response["result"] = []any{map[string]any{
				"uri": uri,
				"range": map[string]any{
					"start": map[string]any{"line": 2, "character": 5},
					"end":   map[string]any{"line": 2, "character": 9}}}}
		case "shutdown":
			response["result"] = json.RawMessage(`null`)
		default:
			response["result"] = map[string]any{"echoed": msg.Method}
		}
		encoded, _ := json.Marshal(response)
		if helperWriteFrame(os.Stdout, encoded) != nil {
			os.Exit(3)
		}
	}
}

func fakeLSPRegistry() *lsp.Registry {
	return lsp.NewRegistry([]lsp.ServerDefinition{{
		ID: "fake", Languages: []string{"go"}, Extensions: []string{".go"},
		Command: os.Args[0], Args: []string{"-test.run=TestLSPRouteHelperProcess", "--"},
		RootMarkers: []string{"go.mod"},
	}})
}

// setupLSPTestServer wires the real WSLSP/LanguageCapabilities routes against a
// git worktree containing one Go file, with a fake language server registered.
// It returns the httptest server, the agent name, and the worktree path.
func setupLSPTestServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()

	worktree := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "qa@example.com"}, {"config", "user.name", "qa"},
	} {
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644))

	cfg := &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}
	db, err := store.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	backend := ptymanager.NewPTYBackend()
	handler := NewSessionsHandler(db, cfg, nil, ptymanager.NewPTYSessionTerminal(backend), nil)
	handler.lspRegistry = fakeLSPRegistry()

	const agent = "lsp-agent"
	require.NoError(t, store.NewGitStore(db).UpsertGitSnapshot(context.Background(), &store.GitSnapshot{
		AgentName: agent, WorkingDirectory: worktree, Branch: "main", CommitHash: "deadbeef",
	}))

	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/lsp", handler.WSLSP)
	r.Get("/api/sessions/live/{name}/language-capabilities", handler.LanguageCapabilities)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, agent, worktree
}

func dialLSP(t *testing.T, server *httptest.Server, agent, filepath string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	url := fmt.Sprintf("%s/api/sessions/live/%s/lsp?filepath=%s",
		strings.Replace(server.URL, "http://", "ws://", 1), agent, filepath)
	conn, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err, "dial LSP websocket")
	t.Cleanup(func() { conn.CloseNow() })
	conn.SetReadLimit(maxBrowserLSPMessage)
	return conn, ctx
}

// TestWSLSPDeliversBrowserRequests is the end-to-end contract for the broker:
// after the ready status envelope, a browser request must produce a response
// envelope. This is the path every hover/definition/references call takes.
func TestWSLSPDeliversBrowserRequests(t *testing.T) {
	server, agent, worktree := setupLSPTestServer(t)
	conn, ctx := dialLSP(t, server, agent, "main.go")

	var status map[string]any
	require.NoError(t, wsjson.Read(ctx, conn, &status), "read ready status envelope")
	require.Equal(t, "status", status["type"])
	require.Equal(t, "ready", status["status"])

	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	require.NoError(t, wsjson.Write(readCtx, conn, map[string]any{
		"type": "request", "id": 1, "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": "go", "version": 1, "text": "package main\n"}},
	}), "send didOpen")

	require.NoError(t, wsjson.Write(readCtx, conn, map[string]any{
		"type": "request", "id": 2, "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 0, "character": 0}},
	}), "send hover")

	var reply map[string]any
	err = wsjson.Read(readCtx, conn, &reply)
	require.NoError(t, err, "the broker must deliver a reply to an allowlisted request")
	require.Equal(t, "response", reply["type"], "expected a response envelope, got %#v", reply)
	require.EqualValues(t, 2, reply["id"])
}

// readReady consumes the initial status envelope so the caller can assert on
// the next message.
func readReady(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	var status map[string]any
	require.NoError(t, wsjson.Read(ctx, conn, &status))
	require.Equal(t, "status", status["type"])
}

// openAndProbe sends didOpen for uri followed by a hover on the same document,
// then returns the first envelope the broker sends back.
//
// didOpen is a notification, so success is silent. Chaining a hover makes the
// outcome observable without a read timeout: a rejected didOpen answers with a
// document_conflict error, an accepted one lets the hover response through.
// (A bare timeout cannot be used as the "accepted" signal — cancelling a
// nhooyr read closes the connection, which would release the very lease under
// test.)
func openAndProbe(t *testing.T, ctx context.Context, conn *websocket.Conn, uri string) map[string]any {
	t.Helper()
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "open", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": "go", "version": 1, "text": "package main\n"}},
	}))
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "probe", "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 0, "character": 0}},
	}))

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var reply map[string]any
	require.NoError(t, wsjson.Read(readCtx, conn, &reply))
	return reply
}

// requireLeaseGranted asserts the connection owns the document lease.
func requireLeaseGranted(t *testing.T, reply map[string]any) {
	t.Helper()
	require.Equal(t, "response", reply["type"], "expected the lease holder's hover to succeed, got %#v", reply)
}

// requireLeaseRefused asserts the broker refused the document with a conflict.
func requireLeaseRefused(t *testing.T, reply map[string]any, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	require.Equal(t, "error", reply["type"], "%s (got %#v)", msg, reply)
	require.Equal(t, "document_conflict", reply["error"].(map[string]any)["code"], msg)
}

// TestWSLSPRejectsSecondClientLease is the baseline for the document-lease
// acceptance criterion: two connections, one file, exactly one owner.
func TestWSLSPRejectsSecondClientLease(t *testing.T) {
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	first, firstCtx := dialLSP(t, server, agent, "main.go")
	readReady(t, firstCtx, first)
	requireLeaseGranted(t, openAndProbe(t, firstCtx, first, uri))

	second, secondCtx := dialLSP(t, server, agent, "main.go")
	readReady(t, secondCtx, second)
	requireLeaseRefused(t, openAndProbe(t, secondCtx, second, uri),
		"second client acquired a concurrent lease on main.go")
}

// TestWSLSPLeaseResistsURIAliasing is the same criterion under aliased URI
// spellings. Manager.Lease keys on the raw browser URI string, so a second
// client can dodge the conflict check while gopls normalizes both spellings to
// one document — the silent-merge case the lease exists to prevent.
func TestWSLSPLeaseResistsURIAliasing(t *testing.T) {
	server, agent, worktree := setupLSPTestServer(t)
	canonicalURI, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	for name, alias := range map[string]string{
		"localhost authority":  strings.Replace(canonicalURI, "file://", "file://localhost", 1),
		"percent-encoded path": strings.Replace(canonicalURI, "main.go", "m%61in.go", 1),
		"dot segment":          strings.Replace(canonicalURI, "/main.go", "/./main.go", 1),
	} {
		t.Run(name, func(t *testing.T) {
			first, firstCtx := dialLSP(t, server, agent, "main.go")
			readReady(t, firstCtx, first)
			requireLeaseGranted(t, openAndProbe(t, firstCtx, first, canonicalURI))

			second, secondCtx := dialLSP(t, server, agent, "main.go")
			readReady(t, secondCtx, second)
			requireLeaseRefused(t, openAndProbe(t, secondCtx, second, alias),
				"second client acquired a concurrent lease on main.go via %s", alias)

			first.CloseNow()
			second.CloseNow()
		})
	}
}

// TestWSLSPRejectsNonAllowlistedMethod covers the "non-allowlisted methods are
// rejected" acceptance criterion over the real socket.
func TestWSLSPRejectsNonAllowlistedMethod(t *testing.T) {
	server, agent, _ := setupLSPTestServer(t)
	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)

	for _, method := range []string{"initialize", "workspace/executeCommand", "textDocument/rename", "shutdown"} {
		require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
			"type": "request", "id": method, "method": method, "params": map[string]any{},
		}))
		var reply map[string]any
		require.NoError(t, wsjson.Read(ctx, conn, &reply), "no reply for %s", method)
		require.Equal(t, "error", reply["type"], "%s was not rejected", method)
		require.Equal(t, "method_not_allowed", reply["error"].(map[string]any)["code"], "method %s", method)
	}
}

// TestWSLSPRejectsOutsideWorkspaceDocumentURI covers request-side path
// containment: a document URI outside the resolved workspace must never reach
// the language server.
func TestWSLSPRejectsOutsideWorkspaceDocumentURI(t *testing.T) {
	server, agent, _ := setupLSPTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.go")
	require.NoError(t, os.WriteFile(outside, []byte("package secret\n"), 0644))
	outsideURI, err := lsp.PathToURI(outside)
	require.NoError(t, err)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)

	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "open", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": outsideURI, "languageId": "go", "version": 1, "text": "package secret\n"}},
	}))

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var reply map[string]any
	require.NoError(t, wsjson.Read(readCtx, conn, &reply), "an out-of-workspace document URI was accepted silently")
	require.Equal(t, "error", reply["type"])
	require.Equal(t, "invalid_path", reply["error"].(map[string]any)["code"])
}

// TestWSLSPCancelIsScopedToOneConnection verifies that one browser cannot
// cancel another browser's in-flight request by guessing its request id.
func TestWSLSPCancelIsScopedToOneConnection(t *testing.T) {
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	victim, victimCtx := dialLSP(t, server, agent, "main.go")
	readReady(t, victimCtx, victim)
	requireLeaseGranted(t, openAndProbe(t, victimCtx, victim, uri))

	attacker, attackerCtx := dialLSP(t, server, agent, "main.go")
	readReady(t, attackerCtx, attacker)

	// The attacker cancels request id 7 without ever having issued it.
	require.NoError(t, wsjson.Write(attackerCtx, attacker, map[string]any{
		"type": "request", "id": 99, "method": "$/cancelRequest",
		"params": map[string]any{"id": 7},
	}))

	// The victim's own request with that id must still complete normally.
	require.NoError(t, wsjson.Write(victimCtx, victim, map[string]any{
		"type": "request", "id": 7, "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 0, "character": 0}},
	}))

	readCtx, cancel := context.WithTimeout(victimCtx, 10*time.Second)
	defer cancel()
	var reply map[string]any
	require.NoError(t, wsjson.Read(readCtx, victim, &reply))
	require.Equal(t, "response", reply["type"], "cross-connection cancel affected another client: %#v", reply)
	require.EqualValues(t, 7, reply["id"])
}

// ── Cancellation ID translation (Task #4) ────────────────────────────
//
// The browser numbers its own requests; Server.RequestMapped allocates an
// independent subprocess JSON-RPC id. A browser cancel must be translated to
// the subprocess id that connection's request actually used, and must never
// reach another connection's request.

// enableCancelLog points the fake server at a file recording every cancelled
// subprocess request id. Must be called before any connection is dialled.
func enableCancelLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cancels.log")
	t.Setenv(cancelLogEnv, path)
	return path
}

// logEntries returns the subprocess request ids the fake server recorded for
// one event kind ("stall" or "cancel"), in order.
func logEntries(t *testing.T, path, kind string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == kind {
			ids = append(ids, fields[1])
		}
	}
	return ids
}

func cancelLog(t *testing.T, path string) []string { return logEntries(t, path, "cancel") }

// stallingRequest sends a request the fake server will never answer until it is
// cancelled, using browserID as the browser-scoped request id.
func stallingRequest(t *testing.T, ctx context.Context, conn *websocket.Conn, uri string, browserID any) {
	t.Helper()
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": browserID, "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 0, "character": 0},
			"stall":        true},
	}))
}

func cancelRequest(t *testing.T, ctx context.Context, conn *websocket.Conn, browserID any) {
	t.Helper()
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "cancel", "method": "$/cancelRequest",
		"params": map[string]any{"id": browserID},
	}))
}

// readEnvelope reads one envelope with a bounded wait. Only use it where a
// message is expected: cancelling a nhooyr read closes the connection.
func readEnvelope(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var reply map[string]any
	require.NoError(t, wsjson.Read(readCtx, conn, &reply))
	return reply
}

// TestWSLSPCancelTranslatesToSubprocessRequestID proves the browser id is
// translated, not forwarded: the id the subprocess is asked to cancel is the
// one it was given, which differs from the browser's.
func TestWSLSPCancelTranslatesToSubprocessRequestID(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	requireLeaseGranted(t, openAndProbe(t, ctx, conn, uri))

	const browserID = 4242
	stallingRequest(t, ctx, conn, uri, browserID)
	cancelRequest(t, ctx, conn, browserID)

	// The fake server answers a cancelled request with the id it was cancelled
	// by, and the broker relays that result under the browser's own id.
	reply := readEnvelope(t, ctx, conn)
	require.Equal(t, "response", reply["type"], "got %#v", reply)
	require.EqualValues(t, browserID, reply["id"], "reply must carry the browser-scoped id")

	cancelled := reply["result"].(map[string]any)["cancelled"]
	require.NotNil(t, cancelled, "the subprocess never saw a cancellation")
	require.NotEqualValues(t, browserID, cancelled,
		"the browser-scoped id was forwarded verbatim instead of being translated")

	require.Equal(t, []string{fmt.Sprintf("%v", int64(cancelled.(float64)))}, cancelLog(t, log),
		"exactly one subprocess request should have been cancelled")
}

// TestWSLSPCancelCannotCrossConnections is the collision case: two connections
// both numbering a request 7. Cancelling on one must not touch the other.
func TestWSLSPCancelCannotCrossConnections(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	// Only one connection may hold the document lease, so give each its own
	// file inside the same workspace and therefore the same server process.
	other := filepath.Join(worktree, "other.go")
	require.NoError(t, os.WriteFile(other, []byte("package main\n"), 0644))
	otherURI, err := lsp.PathToURI(other)
	require.NoError(t, err)

	first, firstCtx := dialLSP(t, server, agent, "main.go")
	readReady(t, firstCtx, first)
	requireLeaseGranted(t, openAndProbe(t, firstCtx, first, uri))

	second, secondCtx := dialLSP(t, server, agent, "other.go")
	readReady(t, secondCtx, second)
	requireLeaseGranted(t, openAndProbe(t, secondCtx, second, otherURI))

	const sharedBrowserID = 7
	stallingRequest(t, firstCtx, first, uri, sharedBrowserID)
	stallingRequest(t, secondCtx, second, otherURI, sharedBrowserID)

	// Only the second connection cancels id 7.
	cancelRequest(t, secondCtx, second, sharedBrowserID)
	secondReply := readEnvelope(t, secondCtx, second)
	require.Equal(t, "response", secondReply["type"])
	secondCancelled := secondReply["result"].(map[string]any)["cancelled"]

	require.Len(t, cancelLog(t, log), 1,
		"cancelling on one connection must cancel exactly one subprocess request")

	// The first connection's request must still be alive: cancelling it now
	// must resolve it, under a different subprocess id.
	cancelRequest(t, firstCtx, first, sharedBrowserID)
	firstReply := readEnvelope(t, firstCtx, first)
	require.Equal(t, "response", firstReply["type"],
		"the other connection's request was cancelled by a foreign cancel")
	firstCancelled := firstReply["result"].(map[string]any)["cancelled"]

	require.NotEqual(t, secondCancelled, firstCancelled,
		"both connections mapped browser id 7 to the same subprocess request")
	require.Len(t, cancelLog(t, log), 2)
}

// TestWSLSPCancelOfUnknownOrCompletedRequestIsHarmless covers the no-op paths:
// they must not error, cancel anything, or disturb the connection.
func TestWSLSPCancelOfUnknownOrCompletedRequestIsHarmless(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	requireLeaseGranted(t, openAndProbe(t, ctx, conn, uri))

	// Never-issued id, then an id that has already completed (the probe hover).
	cancelRequest(t, ctx, conn, 999999)
	cancelRequest(t, ctx, conn, "probe")
	cancelRequest(t, ctx, conn, nil)

	// The connection must still serve requests normally.
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "after", "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 0, "character": 0}},
	}))
	reply := readEnvelope(t, ctx, conn)
	require.Equal(t, "response", reply["type"], "stray cancels disturbed the connection: %#v", reply)
	require.Equal(t, "after", reply["id"])

	require.Empty(t, cancelLog(t, log), "a stray cancel reached the subprocess")
}

// TestWSLSPDisconnectCancelsOutstandingRequests covers the spec requirement
// that "disconnects cancel outstanding browser requests". The browser is gone,
// so the cancellation is observed through the fake server's log.
func TestWSLSPDisconnectCancelsOutstandingRequests(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	requireLeaseGranted(t, openAndProbe(t, ctx, conn, uri))

	stallingRequest(t, ctx, conn, uri, 11)
	stallingRequest(t, ctx, conn, uri, 12)

	// Both requests must actually be in flight before the disconnect, or the
	// test would pass without exercising the cleanup path at all.
	require.Eventually(t, func() bool { return len(logEntries(t, log, "stall")) == 2 },
		10*time.Second, 25*time.Millisecond, "requests never reached the subprocess")
	require.Empty(t, cancelLog(t, log), "nothing should be cancelled before the disconnect")

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, "done"))

	require.Eventually(t, func() bool { return len(cancelLog(t, log)) >= 2 },
		10*time.Second, 50*time.Millisecond,
		"disconnect did not cancel both outstanding requests (log: %v)", cancelLog(t, log))
}

// TestWSLSPRepeatedCancelStillCancels covers a duplicate $/cancelRequest for
// the same browser id.
//
// The broker tracks each browser request in one int64: 0 means "accepted, no
// subprocess id yet", >0 is the subprocess id, and -1 is a tombstone meaning
// "cancel as soon as it registers". A second cancel arriving while the
// tombstone is set takes the "already has an id" branch and deletes the entry,
// and the registration callback then reads the absent key as 0 — indistinguishable
// from "accepted, no id yet" — so it reinserts the entry and never cancels.
// The subprocess request then runs to completion uncancelled.
//
// Cancelling twice is idempotent by intent; it must never be weaker than
// cancelling once.
func TestWSLSPRepeatedCancelStillCancels(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	requireLeaseGranted(t, openAndProbe(t, ctx, conn, uri))

	const rounds = 20
	for i := 0; i < rounds; i++ {
		id := 1000 + i
		stallingRequest(t, ctx, conn, uri, id)
		cancelRequest(t, ctx, conn, id)
		cancelRequest(t, ctx, conn, id)
	}

	require.Eventually(t, func() bool { return len(logEntries(t, log, "stall")) == rounds },
		10*time.Second, 25*time.Millisecond, "requests never reached the subprocess")

	require.Eventually(t, func() bool { return len(cancelLog(t, log)) == rounds },
		10*time.Second, 25*time.Millisecond,
		"a repeated cancel dropped the cancellation: %d of %d requests were left running "+
			"on the language server", rounds-len(cancelLog(t, log)), rounds)
}

// ── Task #8 verification: capability probing and symlinked workspaces ─

func startLog(t *testing.T, path string) []string { return logEntries(t, path, "start") }

// getCapabilities calls the capability endpoint for one file.
func getCapabilities(t *testing.T, server *httptest.Server, agent, file string) map[string]any {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/%s/language-capabilities?filepath=%s",
		server.URL, agent, file))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// TestCapabilityProbeStartsNoSubprocess verifies M3: the capability endpoint
// reports support without launching (or stranding) a language server, however
// many times it is polled. Its doc comment always claimed this; it now holds.
func TestCapabilityProbeStartsNoSubprocess(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, _ := setupLSPTestServer(t)

	for i := 0; i < 5; i++ {
		body := getCapabilities(t, server, agent, "main.go")
		require.Equal(t, "fake", body["server"])
		require.Equal(t, "go", body["language"])
		require.NotEqual(t, "unsupported", body["status"])
	}
	require.Empty(t, startLog(t, log),
		"the capability endpoint spawned %d language server process(es)", len(startLog(t, log)))

	// A real connection must still start exactly one.
	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	require.Len(t, startLog(t, log), 1, "connecting should start exactly one subprocess")
}

// TestCapabilityReportsUnsupportedWithoutStarting covers the other branch: an
// unregistered extension must be reported, not brokered.
func TestCapabilityReportsUnsupportedWithoutStarting(t *testing.T) {
	log := enableCancelLog(t)
	server, agent, worktree := setupLSPTestServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "notes.txt"), []byte("hi\n"), 0644))

	body := getCapabilities(t, server, agent, "notes.txt")
	require.Equal(t, "unsupported", body["status"])
	require.Empty(t, startLog(t, log))
}

// setupSymlinkedLSPServer builds a worktree that the session reaches through a
// symlink, which is how M2 (navigation rejected on symlinked checkouts) arose.
// It returns the server, agent, the symlink path, and the canonical path.
func setupSymlinkedLSPServer(t *testing.T) (*httptest.Server, string, string, string) {
	t.Helper()

	parent := t.TempDir()
	real := filepath.Join(parent, "real-checkout")
	require.NoError(t, os.Mkdir(real, 0755))
	link := filepath.Join(parent, "linked-checkout")
	require.NoError(t, os.Symlink(real, link))

	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "qa@example.com"}, {"config", "user.name", "qa"},
	} {
		require.NoError(t, exec.Command("git", append([]string{"-C", real}, args...)...).Run())
	}
	require.NoError(t, os.WriteFile(filepath.Join(real, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(real, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644))

	cfg := &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}
	db, err := store.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	handler := NewSessionsHandler(db, cfg, nil,
		ptymanager.NewPTYSessionTerminal(ptymanager.NewPTYBackend()), nil)
	handler.lspRegistry = fakeLSPRegistry()

	const agent = "symlink-agent"
	// The session records the path it was launched through — the symlink.
	require.NoError(t, store.NewGitStore(db).UpsertGitSnapshot(context.Background(), &store.GitSnapshot{
		AgentName: agent, WorkingDirectory: link, Branch: "main", CommitHash: "deadbeef",
	}))

	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/lsp", handler.WSLSP)
	r.Get("/api/sessions/live/{name}/language-capabilities", handler.LanguageCapabilities)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	canonical, err := lsp.Canonical(real)
	require.NoError(t, err)
	return srv, agent, link, canonical
}

// TestSymlinkedWorkspaceReportsCanonicalRoot verifies M2's contract: both the
// capability response and the WebSocket ready envelope report one canonical
// workspace root, so the browser can do containment checks against the same
// paths the broker returns.
func TestSymlinkedWorkspaceReportsCanonicalRoot(t *testing.T) {
	server, agent, link, canonical := setupSymlinkedLSPServer(t)
	require.NotEqual(t, link, canonical, "test setup failed to produce a symlinked root")

	body := getCapabilities(t, server, agent, "main.go")
	require.Equal(t, canonical, body["workspace"],
		"capability response must report the canonical root, not the symlinked one")

	conn, ctx := dialLSP(t, server, agent, "main.go")
	var status map[string]any
	require.NoError(t, wsjson.Read(ctx, conn, &status))
	require.Equal(t, "status", status["type"])
	require.Equal(t, canonical, status["workspace"],
		"ready envelope must report the same canonical root as the capability response")
}

// TestSymlinkedWorkspaceAcceptsInWorkspaceNavigation is the behaviour the M2
// finding predicted would break: a definition inside a symlinked checkout must
// be returned, while a genuinely external path is still rejected.
func TestSymlinkedWorkspaceAcceptsInWorkspaceNavigation(t *testing.T) {
	server, agent, _, canonical := setupSymlinkedLSPServer(t)

	conn, ctx := dialLSP(t, server, agent, "main.go")
	readReady(t, ctx, conn)
	uri, err := lsp.PathToURI(filepath.Join(canonical, "main.go"))
	require.NoError(t, err)
	requireLeaseGranted(t, openAndProbe(t, ctx, conn, uri))

	// In-workspace definition, reached through the canonical root.
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "def", "method": "textDocument/definition",
		"params": map[string]any{
			"textDocument":  map[string]any{"uri": uri},
			"position":      map[string]any{"line": 0, "character": 0},
			"definitionURI": uri},
	}))
	reply := readEnvelope(t, ctx, conn)
	require.Equal(t, "response", reply["type"],
		"an in-workspace definition inside a symlinked checkout was rejected: %#v", reply)
	location := reply["result"].([]any)[0].(map[string]any)
	require.Equal(t, uri, location["uri"])

	// A real file outside the workspace must still be refused.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "elsewhere.go")
	require.NoError(t, os.WriteFile(outside, []byte("package elsewhere\n"), 0644))
	outsideURI, err := lsp.PathToURI(outside)
	require.NoError(t, err)

	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "def-outside", "method": "textDocument/definition",
		"params": map[string]any{
			"textDocument":  map[string]any{"uri": uri},
			"position":      map[string]any{"line": 0, "character": 0},
			"definitionURI": outsideURI},
	}))
	rejected := readEnvelope(t, ctx, conn)
	require.Equal(t, "error", rejected["type"], "an out-of-workspace definition was returned to the browser")
	require.Equal(t, "outside_workspace", rejected["error"].(map[string]any)["code"])
}
