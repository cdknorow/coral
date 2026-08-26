package routes

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// ── Optional real-gopls integration ──────────────────────────────────
//
// Everything else in this package proves the broker against a fake language
// server. These tests are the only ones that exercise the real semantic path:
// a real gopls process, real workspace loading, real hover/definition/
// references over the production registry and route.
//
// They skip cleanly when gopls is not installed, as the spec requires. Coral
// never installs it; if the binary lives in GOBIN rather than on PATH, the
// test prepends that directory to PATH for this process only.

const (
	goplsMainFile = `package main

import "fmt"

func main() {
	fmt.Println(Greet("world"))
}
`
	goplsLibFile = `package main

// Greet builds a greeting for the supplied name.
func Greet(name string) string {
	return "hello " + name
}
`
)

// locateGopls returns a directory to prepend to PATH so exec.LookPath("gopls")
// succeeds, or skips the test when no gopls binary exists anywhere obvious.
func locateGopls(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err == nil {
		return
	}
	candidates := []string{}
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		candidates = append(candidates, filepath.Join(gobin, "gopls"))
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidates = append(candidates, filepath.Join(gopath, "bin", "gopls"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "go", "bin", "gopls"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			t.Setenv("PATH", filepath.Dir(candidate)+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
	}
	t.Skip("gopls is not installed; skipping real language-server integration")
}

// setupGoplsServer builds a throwaway two-file Go module where main.go calls a
// function declared in lib.go, and serves it through the production registry.
func setupGoplsServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	locateGopls(t)

	worktree := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "qa@example.com"}, {"config", "user.name", "qa"},
	} {
		require.NoError(t, exec.Command("git", append([]string{"-C", worktree}, args...)...).Run())
	}
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "go.mod"),
		[]byte("module coralqa\n\ngo 1.22\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "main.go"), []byte(goplsMainFile), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "lib.go"), []byte(goplsLibFile), 0644))

	cfg := &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}
	db, err := store.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	handler := NewSessionsHandler(db, cfg, nil,
		ptymanager.NewPTYSessionTerminal(ptymanager.NewPTYBackend()), nil)
	// Production registry and production gopls definition — no fakes here.
	handler.lspRegistry = lsp.DefaultRegistry()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = handler.LSPManager().Close(ctx)
	})

	const agent = "gopls-agent"
	require.NoError(t, store.NewGitStore(db).UpsertGitSnapshot(context.Background(), &store.GitSnapshot{
		AgentName: agent, WorkingDirectory: worktree, Branch: "main", CommitHash: "deadbeef",
	}))

	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/lsp", handler.WSLSP)
	r.Get("/api/sessions/live/{name}/language-capabilities", handler.LanguageCapabilities)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, agent, worktree
}

// positionOf converts the byte offset of needle in text to an LSP position.
// The fixtures are ASCII, so byte offsets and UTF-16 units coincide.
func positionOf(t *testing.T, text, needle string) map[string]any {
	t.Helper()
	index := strings.Index(text, needle)
	require.GreaterOrEqual(t, index, 0, "fixture does not contain %q", needle)
	before := text[:index]
	line := strings.Count(before, "\n")
	character := len(before) - (strings.LastIndex(before, "\n") + 1)
	return map[string]any{"line": line, "character": character}
}

// goplsRequest sends one request and waits for its reply, tolerating the
// notifications and status frames gopls traffic can interleave.
func goplsRequest(t *testing.T, ctx context.Context, conn *websocket.Conn,
	id, method string, params map[string]any) map[string]any {
	t.Helper()
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": id, "method": method, "params": params,
	}))
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		var reply map[string]any
		require.NoError(t, wsjson.Read(readCtx, conn, &reply), "no reply to %s", method)
		if reply["id"] == id {
			return reply
		}
	}
}

// connectGopls opens the socket and synchronises main.go, retrying the first
// semantic request until gopls has finished loading the workspace.
func connectGopls(t *testing.T, srv *httptest.Server, agent, worktree string) (*websocket.Conn, context.Context, string) {
	t.Helper()
	conn, ctx := dialLSP(t, srv, agent, "main.go")

	var status map[string]any
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	require.NoError(t, wsjson.Read(readCtx, conn, &status), "gopls never reported ready")
	require.Equal(t, "ready", status["status"], "status envelope: %#v", status)
	capabilities, _ := status["capabilities"].(map[string]any)
	require.Equal(t, true, capabilities["hover"], "gopls did not advertise hover")
	require.Equal(t, true, capabilities["definition"], "gopls did not advertise definition")
	require.Equal(t, true, capabilities["references"], "gopls did not advertise references")

	uri, err := lsp.PathToURI(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "open", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": "go", "version": 1, "text": goplsMainFile}},
	}))
	return conn, ctx, uri
}

// TestGoplsHoverReturnsSemanticDocumentation covers the acceptance criterion
// "hover displays semantic signature/type documentation" against real gopls.
func TestGoplsHoverReturnsSemanticDocumentation(t *testing.T) {
	srv, agent, worktree := setupGoplsServer(t)
	conn, ctx, uri := connectGopls(t, srv, agent, worktree)

	position := positionOf(t, goplsMainFile, "Greet(\"world\")")

	// gopls answers null until the workspace has loaded; poll to first result.
	var contents string
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		reply := goplsRequest(t, ctx, conn, fmt.Sprintf("hover-%d", attempt), "textDocument/hover",
			map[string]any{"textDocument": map[string]any{"uri": uri}, "position": position})
		require.Equal(t, "response", reply["type"], "hover failed: %#v", reply)
		if result, ok := reply["result"].(map[string]any); ok {
			if value, ok := result["contents"].(map[string]any); ok {
				if text, ok := value["value"].(string); ok && text != "" {
					contents = text
					break
				}
			}
		}
		time.Sleep(time.Second)
	}

	require.NotEmpty(t, contents, "gopls never produced hover content for Greet")
	t.Logf("gopls hover:\n%s", contents)
	require.Contains(t, contents, "Greet", "hover must describe the hovered symbol")
	require.Contains(t, contents, "func Greet(name string) string",
		"hover must carry the real signature, which is the whole point of the feature")
	require.Contains(t, contents, "greeting for the supplied name",
		"hover must carry the doc comment gopls resolved from the other file")
}

// TestGoplsDefinitionResolvesAcrossFiles covers "go to definition opens an
// in-workspace target and selects its range" — the cross-file resolution the
// existing CodeMirror integration cannot do.
func TestGoplsDefinitionResolvesAcrossFiles(t *testing.T) {
	srv, agent, worktree := setupGoplsServer(t)
	conn, ctx, uri := connectGopls(t, srv, agent, worktree)

	position := positionOf(t, goplsMainFile, "Greet(\"world\")")
	libURI, err := lsp.PathToURI(filepath.Join(worktree, "lib.go"))
	require.NoError(t, err)

	var location map[string]any
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		reply := goplsRequest(t, ctx, conn, fmt.Sprintf("def-%d", attempt), "textDocument/definition",
			map[string]any{"textDocument": map[string]any{"uri": uri}, "position": position})
		require.Equal(t, "response", reply["type"], "definition failed: %#v", reply)
		if results, ok := reply["result"].([]any); ok && len(results) > 0 {
			location, _ = results[0].(map[string]any)
			break
		}
		time.Sleep(time.Second)
	}

	require.NotNil(t, location, "gopls never resolved the definition of Greet")
	t.Logf("gopls definition: %#v", location)

	target, _ := location["uri"].(string)
	if target == "" {
		target, _ = location["targetUri"].(string)
	}
	require.Equal(t, libURI, target, "definition must land in the file that declares Greet")

	rangeValue, ok := location["range"].(map[string]any)
	if !ok {
		rangeValue, _ = location["targetSelectionRange"].(map[string]any)
	}
	require.NotNil(t, rangeValue, "definition must carry a range to select")
	start := rangeValue["start"].(map[string]any)
	expected := positionOf(t, goplsLibFile, "Greet(name string)")
	require.EqualValues(t, expected["line"], start["line"],
		"the selected range must be the declaration line in lib.go")
}

// TestGoplsReferencesFindUsagesAcrossFiles covers "find references shows
// grouped, navigable results", including the declaration.
func TestGoplsReferencesFindUsagesAcrossFiles(t *testing.T) {
	srv, agent, worktree := setupGoplsServer(t)
	conn, ctx, uri := connectGopls(t, srv, agent, worktree)

	position := positionOf(t, goplsMainFile, "Greet(\"world\")")
	libURI, err := lsp.PathToURI(filepath.Join(worktree, "lib.go"))
	require.NoError(t, err)

	var results []any
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		reply := goplsRequest(t, ctx, conn, fmt.Sprintf("ref-%d", attempt), "textDocument/references",
			map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     position,
				"context":      map[string]any{"includeDeclaration": true}})
		require.Equal(t, "response", reply["type"], "references failed: %#v", reply)
		if list, ok := reply["result"].([]any); ok && len(list) > 0 {
			results = list
			break
		}
		time.Sleep(time.Second)
	}

	require.NotEmpty(t, results, "gopls never returned references for Greet")
	t.Logf("gopls returned %d references", len(results))

	found := map[string]bool{}
	for _, entry := range results {
		location := entry.(map[string]any)
		target, _ := location["uri"].(string)
		found[target] = true
	}
	require.True(t, found[libURI], "references must include the declaration in lib.go")
	require.True(t, found[uri], "references must include the call site in main.go")
}

// TestGoplsUnsavedBufferDrivesResults is the criterion most likely to regress
// silently: semantic results must reflect the editor's unsaved text, not the
// file on disk.
func TestGoplsUnsavedBufferDrivesResults(t *testing.T) {
	srv, agent, worktree := setupGoplsServer(t)
	conn, ctx, uri := connectGopls(t, srv, agent, worktree)

	// Warm up so gopls has loaded the package before the edit.
	warmup := positionOf(t, goplsMainFile, "Greet(\"world\")")
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		reply := goplsRequest(t, ctx, conn, fmt.Sprintf("warm-%d", attempt), "textDocument/hover",
			map[string]any{"textDocument": map[string]any{"uri": uri}, "position": warmup})
		if result, ok := reply["result"].(map[string]any); ok && result["contents"] != nil {
			break
		}
		time.Sleep(time.Second)
	}

	// Rename the call in the unsaved buffer only; lib.go on disk is untouched.
	edited := strings.Replace(goplsMainFile, `Greet("world")`, `Missing("world")`, 1)
	require.NoError(t, wsjson.Write(ctx, conn, map[string]any{
		"type": "request", "id": "change", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": 2},
			"contentChanges": []any{map[string]any{"text": edited}}},
	}))

	// The identifier at that position is now undefined, so gopls must stop
	// resolving it — proving it is reading the unsaved buffer.
	position := positionOf(t, edited, `Missing("world")`)
	resolved := true
	for attempt := 0; time.Now().Before(time.Now().Add(30 * time.Second)) && attempt < 20; attempt++ {
		reply := goplsRequest(t, ctx, conn, fmt.Sprintf("unsaved-%d", attempt), "textDocument/definition",
			map[string]any{"textDocument": map[string]any{"uri": uri}, "position": position})
		if reply["type"] == "error" {
			resolved = false
			break
		}
		if list, ok := reply["result"].([]any); !ok || len(list) == 0 {
			resolved = false
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.False(t, resolved,
		"gopls still resolved a symbol that exists only in the on-disk file; "+
			"the unsaved buffer is not reaching the language server")

	// The file on disk is still valid, confirming the edit was buffer-only.
	onDisk, err := os.ReadFile(filepath.Join(worktree, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(onDisk), `Greet("world")`)
}
