package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/hubproxy"
)

// An artifact's bytes and type come from an agent. Opening its link must never run
// script on the dashboard's origin: every response is sandboxed and never sniffed,
// and anything that is not inert media or plain data downloads instead of opening.
func TestAgentArtifactsNeverRunOnTheDashboardOrigin(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	_, h, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "artifact-session", AgentName: "artifact-agent", AgentType: "codex", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	h.RegisterAgentArtifacts(r)
	server := httptest.NewServer(r)
	defer server.Close()

	upload := func(name, declared, body string) string {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/artifacts?session_id=artifact-session", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Artifact-Name", name)
		if declared != "" {
			req.Header.Set("X-Artifact-Media-Type", declared)
		}
		resp, err := agentClient.Do(req)
		require.NoError(t, err)
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(data))
		var out struct {
			URL string `json:"url"`
		}
		require.NoError(t, json.Unmarshal(data, &out))
		return out.URL
	}

	cases := []struct {
		name, declared, body string
		wantType             string // the media type is kept for the preview pane
		inline               bool
	}{
		{"page.html", "", "<script>parent.pwned=1</script>", "text/html", false},
		{"page.xhtml", "application/xhtml+xml", "<html xmlns='http://www.w3.org/1999/xhtml'><script>1</script></html>", "application/xhtml+xml", false},
		{"logo.svg", "", "<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>", "image/svg+xml", false},
		{"feed.xml", "text/xml", "<x/>", "text/xml", false},
		{"app.js", "text/javascript", "alert(1)", "text/javascript", false},
		{"report.pdf", "application/pdf", "%PDF-1.4", "application/pdf", false},
		{"report.md", "text/markdown", "# hi", "text/markdown", false},
		// An agent can declare an innocent name with an active type, or the reverse.
		{"innocent.png", "text/html", "<script>1</script>", "text/html", false},
		{"bad-type", "not a media type", "x", "application/octet-stream", false},
		{"notes.txt", "text/plain", "hello", "text/plain", true},
		{"shot.png", "image/png", "\x89PNG\r\n", "image/png", true},
		{"data.json", "application/json", "{}", "application/json", true},
		{"blob.bin", "application/octet-stream", "\x00\x01", "application/octet-stream", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := upload(c.name, c.declared, c.body)
			require.True(t, strings.HasSuffix(url, "?v=2"), "links use the v1.4.12 form: %s", url)
			resp, err := http.Get(server.URL + url)
			require.NoError(t, err)
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, c.body, string(data), "the bytes are served unchanged")
			require.Equal(t, wantCSP(c.wantType), resp.Header.Get("Content-Security-Policy"))
			require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
			// The pre-1.4.12 bare link still works, with the same protection.
			bare, err := http.Get(server.URL + strings.SplitN(url, "?", 2)[0])
			require.NoError(t, err)
			bare.Body.Close()
			require.Equal(t, http.StatusOK, bare.StatusCode)
			require.Equal(t, wantCSP(c.wantType), bare.Header.Get("Content-Security-Policy"))
			require.Equal(t, resp.Header.Get("Content-Disposition"), bare.Header.Get("Content-Disposition"))
			ct := strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]
			require.Equal(t, c.wantType, ct)
			cd := resp.Header.Get("Content-Disposition")
			if c.inline {
				require.True(t, strings.HasPrefix(cd, "inline"), cd)
			} else {
				require.True(t, strings.HasPrefix(cd, "attachment"), "an active or unknown type must download, got %q", cd)
			}
		})
	}
}

func TestUntrustedContentHeadersSanitiseTheFilename(t *testing.T) {
	h := http.Header{}
	setUntrustedContentHeaders(h, "text/html", `../x"; filename=evil.html`)
	cd := h.Get("Content-Disposition")
	require.True(t, strings.HasPrefix(cd, "attachment; filename="), cd)
	require.NotContains(t, cd, `"`)
	require.NotContains(t, cd, "/")
	require.Equal(t, 1, strings.Count(cd, "filename="), cd)
}

// A workflow step (a shell command or an agent) writes its step directory, under any
// name, and the run view links to those files.
func TestWorkflowStepFilesNeverRunOnTheDashboardOrigin(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Load(dataDir)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	ctx := context.Background()
	wf, err := h.ws.CreateWorkflow(ctx, &store.Workflow{Name: "files", StepsJSON: `[{"name":"one","type":"shell","command":"true"}]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	run, err := h.ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	stepDir := filepath.Join(cfg.CoralDir(), "workflows", "runs", fmt.Sprint(run.ID), "step_0")
	require.NoError(t, os.MkdirAll(stepDir, 0o755))
	files := map[string]string{
		"out.html":   "<script>parent.pwned=1</script>",
		"logo.svg":   "<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>",
		"stdout.txt": "hello",
		"exit_code":  "3",
		"noext-html": "<!DOCTYPE html><html><script>1</script></html>",
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(stepDir, name), []byte(body), 0o644))
	}

	r := chi.NewRouter()
	r.Get("/api/workflows/runs/{runID}/steps/{stepIndex}/files/*", h.GetStepFile)
	server := httptest.NewServer(r)
	defer server.Close()

	cases := []struct {
		name, wantType string
		inline         bool
	}{
		{"out.html", "text/html", false},
		{"logo.svg", "image/svg+xml", false},
		{"stdout.txt", "text/plain", true},
		{"exit_code", "text/plain", true},
		// Sniffed HTML with no extension is shown as text, never as a page.
		{"noext-html", "text/plain", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(fmt.Sprintf("%s/api/workflows/runs/%d/steps/0/files/%s", server.URL, run.ID, c.name))
			require.NoError(t, err)
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, string(data))
			require.Equal(t, files[c.name], string(data))
			require.Equal(t, wantCSP(c.wantType), resp.Header.Get("Content-Security-Policy"))
			require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
			require.Equal(t, c.wantType, strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
			cd := resp.Header.Get("Content-Disposition")
			if c.inline {
				require.True(t, strings.HasPrefix(cd, "inline"), cd)
			} else {
				require.True(t, strings.HasPrefix(cd, "attachment"), cd)
			}
		})
	}
}

// wantCSP: an HTML document gets the preview policy (its inline styles apply, #3593);
// everything else the plain sandbox.
func wantCSP(mediaType string) string {
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return hubproxy.PreviewCSP
	}
	return hubproxy.SandboxCSP
}

// #3593: the preview policy still loads nothing and runs nothing: no script source, no
// connect, frame or other host source, data: only for fonts and images, and a sandbox
// without allow-scripts.
func TestPreviewCSPAllowsOnlyInlineStyle(t *testing.T) {
	p := hubproxy.PreviewCSP
	require.True(t, strings.HasPrefix(p, "sandbox;"), "a sandbox with no allow- tokens: %s", p)
	// 'self' appears once, as the frame-ancestors source: never as a source the document loads from.
	loads := strings.Replace(p, "frame-ancestors 'self'", "", 1)
	for _, forbidden := range []string{"allow-", "script-src", "connect-src", "frame-src", "http", "*", "'self'", "unsafe-eval", "blob:"} {
		require.NotContains(t, loads, forbidden, p)
	}
	for _, want := range []string{"default-src 'none'", "style-src 'unsafe-inline'", "font-src data:", "img-src data:", "frame-ancestors 'self'"} {
		require.Contains(t, p, want, p)
	}
}
