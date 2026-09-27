package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateBoardEnv makes a test hermetic with respect to the agent environment.
//
// Coral exports CORAL_DATA_DIR, CORAL_DIR, CORAL_SESSION_NAME and
// CORAL_SUBSCRIBER_ID into every agent shell, and coralDir() prefers the data
// dir variables over HOME. Redirecting HOME alone therefore left these tests
// reading, overwriting and deleting the real board_state file of whichever
// agent ran them. Any test that touches the state file, coralDir() or identity
// resolution must call this first. It returns the temporary data directory.
func isolateBoardEnv(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_DATA_DIR", dataDir)
	t.Setenv("CORAL_DIR", dataDir)
	t.Setenv("CORAL_SESSION_NAME", "coral-board-test-session")
	t.Setenv("CORAL_SUBSCRIBER_ID", "")
	t.Setenv("TMUX", "")

	// loadState falls back to PID resolution, which asks a live Coral server
	// to identify the calling process tree. Inside an agent shell that
	// succeeds, so "missing state" would never be observed. Mark it as already
	// resolved to nothing, and restore the package globals afterwards.
	oldDone, oldCached, oldURL := pidResolutionDone, cachedPIDResolution, serverURL
	pidResolutionDone, cachedPIDResolution = true, nil
	t.Cleanup(func() {
		pidResolutionDone, cachedPIDResolution, serverURL = oldDone, oldCached, oldURL
	})
	return dataDir
}

// TestIsolateBoardEnv_RedirectsStateFile guards the helper itself: the state
// file must resolve inside the temporary data dir even when the process
// inherited CORAL_DATA_DIR and CORAL_DIR pointing at a real install.
func TestIsolateBoardEnv_RedirectsStateFile(t *testing.T) {
	t.Setenv("CORAL_DATA_DIR", "/nonexistent/real-coral-dir")
	t.Setenv("CORAL_DIR", "/nonexistent/real-coral-dir")
	dataDir := isolateBoardEnv(t)

	if got := coralDir(); got != dataDir {
		t.Fatalf("coralDir() = %q, want temp dir %q", got, dataDir)
	}
	want := filepath.Join(dataDir, "board_state_coral-board-test-session.json")
	if got := stateFilePath(); got != want {
		t.Fatalf("stateFilePath() = %q, want %q", got, want)
	}
}

// --- resolveSessionName tests ---

func TestResolveSessionName_FallsBackToHostname(t *testing.T) {
	// Without CORAL_SESSION_NAME or TMUX, should fall back to hostname.
	// Both are set in every agent shell, so clear them explicitly.
	t.Setenv("CORAL_SESSION_NAME", "")
	t.Setenv("TMUX", "")

	name := resolveSessionName()
	if name == "" {
		t.Error("resolveSessionName returned empty string")
	}
	host, _ := os.Hostname()
	if name != host {
		t.Errorf("expected hostname %q, got %q", host, name)
	}
}

// --- State file management tests ---

func TestStateFile_SaveAndLoad(t *testing.T) {
	isolateBoardEnv(t)

	// Save state
	st := &boardState{Project: "test-project", JobTitle: "QA Engineer"}
	saveState(st)

	// Load it back
	loaded := loadState()
	if loaded == nil {
		t.Fatal("loadState returned nil")
	}
	if loaded.Project != "test-project" {
		t.Errorf("Project = %q, want %q", loaded.Project, "test-project")
	}
	if loaded.JobTitle != "QA Engineer" {
		t.Errorf("JobTitle = %q, want %q", loaded.JobTitle, "QA Engineer")
	}

	// Delete state
	deleteState()
	if loadState() != nil {
		t.Error("state should be nil after delete")
	}
}

func TestStateFile_LoadMissing(t *testing.T) {
	isolateBoardEnv(t)

	st := loadState()
	if st != nil {
		t.Error("expected nil for missing state file")
	}
}

func TestStateFile_ServerURLOverride(t *testing.T) {
	isolateBoardEnv(t)

	// Save state with custom server URL
	st := &boardState{Project: "test", JobTitle: "Dev", ServerURL: "http://custom:9999"}
	saveState(st)

	// loadState overrides serverURL; isolateBoardEnv restores it on cleanup.
	loaded := loadState()
	if loaded == nil {
		t.Fatal("loadState returned nil")
	}
	if serverURL != "http://custom:9999" {
		t.Errorf("serverURL = %q, want %q", serverURL, "http://custom:9999")
	}
}

func TestStateFilePath_ContainsSessionName(t *testing.T) {
	isolateBoardEnv(t)
	path := stateFilePath()
	if !filepath.IsAbs(path) {
		t.Errorf("state file path should be absolute, got %q", path)
	}
	if filepath.Ext(path) != ".json" {
		t.Errorf("state file should have .json extension, got %q", path)
	}
	if base := filepath.Base(path); base != "board_state_coral-board-test-session.json" {
		t.Errorf("state file name = %q, want it to contain the session name", base)
	}
}

// --- apiCall tests with mock server ---

func TestApiCall_GET(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/board/test-project/subscribers" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	result, err := apiCall("GET", "/test-project/subscribers", nil)
	if err != nil {
		t.Fatalf("apiCall failed: %v", err)
	}
	if result["ok"] != true {
		t.Error("expected ok=true in response")
	}
}

func TestApiCall_POST_WithBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type: application/json")
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["session_id"] != "test-session" {
			t.Errorf("session_id = %q, want %q", body["session_id"], "test-session")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": float64(42)})
	}))
	defer ts.Close()

	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	result, err := apiCall("POST", "/my-board/messages", map[string]string{
		"session_id": "test-session",
		"content":    "hello",
	})
	if err != nil {
		t.Fatalf("apiCall failed: %v", err)
	}
	if result["id"] != float64(42) {
		t.Errorf("expected id=42, got %v", result["id"])
	}
}

func TestApiCall_ServerDown(t *testing.T) {
	oldURL := serverURL
	serverURL = "http://localhost:1" // nothing listens here
	defer func() { serverURL = oldURL }()

	_, err := apiCall("GET", "/projects", nil)
	if err == nil {
		t.Error("expected error when server is down")
	}
}

// --- Init / env var tests ---

func TestInit_CORAL_URL(t *testing.T) {
	oldURL := serverURL
	defer func() { serverURL = oldURL }()

	// The init() already ran, but we can test the logic directly
	t.Setenv("CORAL_URL", "http://custom-host:9000/")
	serverURL = "http://localhost:8420"
	if v := os.Getenv("CORAL_URL"); v != "" {
		serverURL = v[:len(v)-1] // trim trailing slash
	}
	if serverURL != "http://custom-host:9000" {
		t.Errorf("serverURL = %q, want %q", serverURL, "http://custom-host:9000")
	}
}

func TestInit_CORAL_PORT(t *testing.T) {
	oldURL := serverURL
	defer func() { serverURL = oldURL }()

	t.Setenv("CORAL_PORT", "9999")
	if v := os.Getenv("CORAL_PORT"); v != "" {
		serverURL = "http://localhost:" + v
	}
	if serverURL != "http://localhost:9999" {
		t.Errorf("serverURL = %q, want %q", serverURL, "http://localhost:9999")
	}
}

// --- printUsage doesn't panic ---

func TestPrintUsage_NoPanic(t *testing.T) {
	// Just verify it doesn't panic
	printUsage()
}

func TestCmdRead_URLRouting(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantPath   string
		wantQuery  string
	}{
		{
			name:      "default read without flags",
			args:      []string{"coral-board", "read"},
			wantPath:  "/api/board/test-proj/messages",
			wantQuery: "limit=50&subscriber_id=Dev",
		},
		{
			name:      "--all flag includes all=true",
			args:      []string{"coral-board", "read", "--all"},
			wantPath:  "/api/board/test-proj/messages",
			wantQuery: "all=true&limit=50&subscriber_id=Dev",
		},
		{
			name:      "-a flag includes all=true",
			args:      []string{"coral-board", "read", "-a"},
			wantPath:  "/api/board/test-proj/messages",
			wantQuery: "all=true&limit=50&subscriber_id=Dev",
		},
		{
			name:      "--last N routes to /messages/all",
			args:      []string{"coral-board", "read", "--last", "10"},
			wantPath:  "/api/board/test-proj/messages/all",
			wantQuery: "limit=10",
		},
		{
			name:      "--id N routes to /messages/all?id=N",
			args:      []string{"coral-board", "read", "--id", "42"},
			wantPath:  "/api/board/test-proj/messages/all",
			wantQuery: "id=42",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateBoardEnv(t)
			saveState(&boardState{Project: "test-proj", JobTitle: "Dev"})

			var recordedPath, recordedRawQuery string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recordedPath = r.URL.Path
				recordedRawQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("id") != "" {
					w.Write([]byte(`[{"id":42,"content":"hello"}]`))
				} else {
					w.Write([]byte("[]"))
				}
			}))
			defer ts.Close()

			oldURL := serverURL
			serverURL = ts.URL
			defer func() { serverURL = oldURL }()

			oldArgs := os.Args
			os.Args = tc.args
			defer func() { os.Args = oldArgs }()

			cmdRead()

			if recordedPath != tc.wantPath {
				t.Errorf("Path = %q, want %q", recordedPath, tc.wantPath)
			}
			// Compare query parameters
			for _, part := range strings.Split(tc.wantQuery, "&") {
				if !strings.Contains(recordedRawQuery, part) {
					t.Errorf("RawQuery %q does not contain %q", recordedRawQuery, part)
				}
			}
		})
	}
}
