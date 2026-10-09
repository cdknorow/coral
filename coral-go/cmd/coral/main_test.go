package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/server"
	"github.com/cdknorow/coral/internal/store"
)

func TestStandardBuildEncryptionSelfTestUnavailable(t *testing.T) {
	if dbcrypt.FeatureAvailable() {
		t.Skip("standard build contract")
	}
	if err := runEncryptionSelfTest(); err == nil {
		t.Fatal("standard build must reject encrypted self-test")
	}
}

// setupTestServer creates a test Coral server with an isolated DB.
func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return setupTestServerWithHub(t, false)
}

func setupTestServerWithHub(t *testing.T, hub bool) *httptest.Server {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := &config.Config{
		DBPath:          filepath.Join(tmpDir, "test.db"),
		Host:            "127.0.0.1",
		Port:            0,
		LogDir:          tmpDir,
		WSPollIntervalS: 1,
		CoralRoot:       tmpDir,
		HubMode:         hub,
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ptyBackend := ptymanager.NewPTYBackend()
	terminal := ptymanager.NewPTYSessionTerminal(ptyBackend)
	srv := server.New(cfg, db, ptyBackend, terminal)

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// --- Server health tests ---

func TestServer_RootServesHTML(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/html; charset=utf-8" && ct != "text/html" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestServer_StaticAssets(t *testing.T) {
	ts := setupTestServer(t)

	paths := []string{
		"/static/app.js",
		"/static/api.js",
		"/static/state.js",
		"/static/css/base.css",
		"/static/favicon.png",
	}

	for _, path := range paths {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Errorf("GET %s failed: %v", path, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

// --- API endpoint smoke tests ---

func TestServer_APILiveSessions(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/sessions/live")
	if err != nil {
		t.Fatalf("GET /api/sessions/live failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var sessions []any
	json.NewDecoder(resp.Body).Decode(&sessions)
	// Should return empty array, not null
	if sessions == nil {
		t.Error("expected empty array, got nil")
	}
}

func TestServer_APISettings(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["settings"]; !ok {
		t.Error("expected 'settings' key in response")
	}
}

func TestServer_APITags(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/tags")
	if err != nil {
		t.Fatalf("GET /api/tags failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServer_APIThemes(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/themes")
	if err != nil {
		t.Fatalf("GET /api/themes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServer_APIBoardProjects(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/board/projects")
	if err != nil {
		t.Fatalf("GET /api/board/projects failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServer_APIWebhooks(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/webhooks")
	if err != nil {
		t.Fatalf("GET /api/webhooks failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServer_APIDefaultPrompts(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/settings/default-prompts")
	if err != nil {
		t.Fatalf("GET /api/default-prompts failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["default_prompt_orchestrator"]; !ok {
		t.Error("expected 'default_prompt_orchestrator' key in response")
	}
}

func TestServer_404OnUnknownRoute(t *testing.T) {
	ts := setupTestServer(t)

	resp, err := http.Get(ts.URL + "/api/nonexistent-endpoint")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		t.Error("expected non-200 for unknown route")
	}
}

// --- WebSocket endpoint exists ---

func TestServer_WSCoralEndpointExists(t *testing.T) {
	ts := setupTestServer(t)

	// Just verify the endpoint accepts upgrades (will fail without proper WS handshake, but shouldn't 404)
	resp, err := http.Get(ts.URL + "/ws/coral")
	if err != nil {
		t.Fatalf("GET /ws/coral failed: %v", err)
	}
	defer resp.Body.Close()

	// Should get 400 (bad request - not a WS upgrade) not 404
	if resp.StatusCode == 404 {
		t.Error("/ws/coral returned 404 — route not registered")
	}
}

func TestEnvTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !envTruthy(v) {
			t.Errorf("envTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if envTruthy(v) {
			t.Errorf("envTruthy(%q) = true, want false", v)
		}
	}
}

// Hub features exist only when the server runs with --hub (or CORAL_HUB=1).
func TestServer_HubModeGatesRemoteRoutes(t *testing.T) {
	get := func(ts *httptest.Server, path string) int {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	health := func(ts *httptest.Server) bool {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/health")
		if err != nil {
			t.Fatalf("GET /api/health: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Hub bool `json:"hub"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		return body.Hub
	}

	off := setupTestServerWithHub(t, false)
	if health(off) {
		t.Error("health reports hub=true without --hub")
	}
	for _, p := range []string{"/api/servers", "/api/remote/x/api/health"} {
		if code := get(off, p); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("hub off: GET %s = %d, want 404/405", p, code)
		}
	}

	on := setupTestServerWithHub(t, true)
	if !health(on) {
		t.Error("health does not report hub=true with --hub")
	}
	if code := get(on, "/api/servers"); code != http.StatusOK {
		t.Errorf("hub on: GET /api/servers = %d, want 200", code)
	}
	if code := get(on, "/api/remote/missing/api/health"); code != http.StatusNotFound {
		t.Errorf("hub on: unknown server = %d, want 404", code)
	}
}
