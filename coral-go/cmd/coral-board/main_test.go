package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestEndpointFromEnv_PreservesExplicitURLAndSupportsHostPortFallback(t *testing.T) {
	tests := []struct {
		name, explicit, host, port, want string
	}{
		{"explicit URL wins", "https://coral.example:9443///", "127.0.0.1", "8420", "https://coral.example:9443"},
		{"host and port", "", "127.0.0.1", "8455", "http://127.0.0.1:8455"},
		{"IPv6 host", "", "::1", "8420", "http://[::1]:8420"},
		{"wildcard host", "", "0.0.0.0", "8420", "http://127.0.0.1:8420"},
		{"legacy defaults", "", "", "", "http://localhost:8420"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := endpointFromEnv(tt.explicit, tt.host, tt.port); got != tt.want {
				t.Fatalf("endpointFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubprocess_EndpointInitialization(t *testing.T) {
	if os.Getenv("TEST_SUBPROCESS_ENDPOINT") == "1" {
		fmt.Println("RESOLVED_SERVER_URL=" + serverURL)
		os.Exit(0)
	}

	tests := []struct {
		name      string
		coralURL  string
		coralHost string
		coralPort string
		wantURL   string
	}{
		{
			name:      "explicit_url_wins_over_port_and_host",
			coralURL:  "http://custom-server:9000",
			coralHost: "127.0.0.1",
			coralPort: "8420",
			wantURL:   "http://custom-server:9000",
		},
		{
			name:      "host_and_port_fallback",
			coralURL:  "",
			coralHost: "127.0.0.1",
			coralPort: "8455",
			wantURL:   "http://127.0.0.1:8455",
		},
		{
			name:      "wildcard_host_normalized",
			coralURL:  "",
			coralHost: "0.0.0.0",
			coralPort: "8420",
			wantURL:   "http://127.0.0.1:8420",
		},
		{
			name:      "ipv6_host_bracketed",
			coralURL:  "",
			coralHost: "::1",
			coralPort: "8420",
			wantURL:   "http://[::1]:8420",
		},
		{
			name:      "default_legacy",
			coralURL:  "",
			coralHost: "",
			coralPort: "",
			wantURL:   "http://localhost:8420",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocess_EndpointInitialization$")
			cmd.Env = append(os.Environ(),
				"TEST_SUBPROCESS_ENDPOINT=1",
				"CORAL_URL="+tt.coralURL,
				"CORAL_HOST="+tt.coralHost,
				"CORAL_PORT="+tt.coralPort,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("subprocess failed: %v\noutput: %s", err, string(out))
			}
			expectedLine := "RESOLVED_SERVER_URL=" + tt.wantURL
			if !strings.Contains(string(out), expectedLine) {
				t.Fatalf("expected output to contain %q, got output:\n%s", expectedLine, string(out))
			}
		})
	}
}

func TestEndpointPrecedence_ChildEnvironmentAndStateFile(t *testing.T) {
	t.Run("child_environment_missing_coral_url_loads_persisted_server_url", func(t *testing.T) {
		isolateBoardEnv(t)
		t.Setenv("CORAL_URL", "")
		t.Setenv("CORAL_PORT", "")

		// Simulate post-fix persisted board state written by sessions.go
		st := &boardState{
			Project:   "death-or-trade-ai-auto",
			JobTitle:  "Frontend Dev",
			ServerURL: "http://127.0.0.1:8420",
		}
		saveState(st)

		loaded := loadState()
		if loaded == nil {
			t.Fatal("expected loadState to load state file")
		}
		if loaded.Project != "death-or-trade-ai-auto" {
			t.Errorf("Project = %q, want 'death-or-trade-ai-auto'", loaded.Project)
		}
		if loaded.JobTitle != "Frontend Dev" {
			t.Errorf("JobTitle = %q, want 'Frontend Dev'", loaded.JobTitle)
		}
		if serverURL != "http://127.0.0.1:8420" {
			t.Errorf("serverURL = %q, want 'http://127.0.0.1:8420'", serverURL)
		}
	})

	t.Run("legacy_existing_session_without_server_url_retains_localhost_fallback", func(t *testing.T) {
		isolateBoardEnv(t)
		t.Setenv("CORAL_URL", "")
		t.Setenv("CORAL_PORT", "")
		serverURL = "http://localhost:8420"

		// Simulate pre-fix legacy board state where server_url was omitted on default port
		st := &boardState{
			Project:   "death-or-trade-ai-auto",
			JobTitle:  "Frontend Dev",
			ServerURL: "",
		}
		saveState(st)

		loaded := loadState()
		if loaded == nil {
			t.Fatal("expected loadState to load state file")
		}
		if loaded.Project != "death-or-trade-ai-auto" {
			t.Errorf("Project = %q, want 'death-or-trade-ai-auto'", loaded.Project)
		}
		if serverURL != "http://localhost:8420" {
			t.Errorf("serverURL = %q, want 'http://localhost:8420' (fallback preserved)", serverURL)
		}
	})

	t.Run("state_file_server_url_takes_precedence_over_prior_serverURL", func(t *testing.T) {
		isolateBoardEnv(t)
		serverURL = "http://prior-env-url:8420"

		st := &boardState{
			Project:   "my-proj",
			JobTitle:  "Backend Dev",
			ServerURL: "http://127.0.0.1:9090",
		}
		saveState(st)

		loaded := loadState()
		if loaded == nil {
			t.Fatal("expected loadState to load state file")
		}
		if serverURL != "http://127.0.0.1:9090" {
			t.Errorf("serverURL = %q, want 'http://127.0.0.1:9090' from state file", serverURL)
		}
	})
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
		name      string
		args      []string
		wantPath  string
		wantQuery string
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

func TestCmdWait_Register(t *testing.T) {
	isolateBoardEnv(t)
	saveState(&boardState{Project: "wait-proj", JobTitle: "Dev"})

	var recordedMethod, recordedPath string
	var recordedBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedMethod = r.Method
		recordedPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&recordedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1,"status":"active","expires_at":"2026-09-27T12:00:00Z"}`))
	}))
	defer ts.Close()

	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	oldArgs := os.Args
	os.Args = []string{"coral-board", "wait", "--from", "Orchestrator", "--reason", "waiting for accepted revision"}
	defer func() { os.Args = oldArgs }()

	cmdWait()

	if recordedMethod != "POST" {
		t.Errorf("Method = %q, want POST", recordedMethod)
	}
	if recordedPath != "/api/board/wait-proj/waits" {
		t.Errorf("Path = %q, want /api/board/wait-proj/waits", recordedPath)
	}
	if recordedBody["wait_type"] != "message" {
		t.Errorf("wait_type = %q, want message", recordedBody["wait_type"])
	}
	if recordedBody["target_id"] != "Orchestrator" {
		t.Errorf("target_id = %q, want Orchestrator", recordedBody["target_id"])
	}
	if recordedBody["reason"] != "waiting for accepted revision" {
		t.Errorf("reason = %q, want 'waiting for accepted revision'", recordedBody["reason"])
	}
}

func TestCmdWait_TaskRegister(t *testing.T) {
	isolateBoardEnv(t)
	saveState(&boardState{Project: "wait-proj", JobTitle: "Dev"})

	var recordedBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&recordedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":2,"status":"active","expires_at":"2026-09-27T12:00:00Z"}`))
	}))
	defer ts.Close()

	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	oldArgs := os.Args
	os.Args = []string{"coral-board", "wait", "--task", "876"}
	defer func() { os.Args = oldArgs }()

	cmdWait()

	if recordedBody["wait_type"] != "task" {
		t.Errorf("wait_type = %q, want task", recordedBody["wait_type"])
	}
	if recordedBody["target_id"] != "876" {
		t.Errorf("target_id = %q, want 876", recordedBody["target_id"])
	}
}

func TestCmdWait_StatusAndCancel(t *testing.T) {
	isolateBoardEnv(t)
	saveState(&boardState{Project: "wait-proj", JobTitle: "Dev"})

	var recordedMethods []string
	var recordedPaths []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedMethods = append(recordedMethods, r.Method)
		recordedPaths = append(recordedPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"wait":{"wait_type":"message","target_id":"Lead","reason":"review","expires_at":"2026-09-27T12:00:00Z","status":"active"}}`))
		} else if r.Method == "DELETE" {
			w.Write([]byte(`{"cancelled":true}`))
		}
	}))
	defer ts.Close()

	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"coral-board", "wait", "--status"}
	cmdWait()

	os.Args = []string{"coral-board", "wait", "--cancel"}
	cmdWait()

	if len(recordedMethods) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(recordedMethods))
	}
	if recordedMethods[0] != "GET" || recordedPaths[0] != "/api/board/wait-proj/waits" {
		t.Errorf("call 0: %s %s, want GET /api/board/wait-proj/waits", recordedMethods[0], recordedPaths[0])
	}
	if recordedMethods[1] != "DELETE" || recordedPaths[1] != "/api/board/wait-proj/waits" {
		t.Errorf("call 1: %s %s, want DELETE /api/board/wait-proj/waits", recordedMethods[1], recordedPaths[1])
	}
}

func TestRequestURLs_LocalhostAddsIPv4RetryOnly(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"localhost", "http://localhost:8420/api/board/p/tasks", []string{"http://localhost:8420/api/board/p/tasks", "http://127.0.0.1:8420/api/board/p/tasks"}},
		{"explicit IPv4", "http://127.0.0.1:8420/api", []string{"http://127.0.0.1:8420/api"}},
		{"remote host", "http://coral.example/api", []string{"http://coral.example/api"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requestURLs(tt.in)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("requestURLs(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRetryLoopbackTransportDoesNotDuplicateGenericPost(t *testing.T) {
	if retryLoopbackTransport(http.MethodPost, syscall.ECONNREFUSED) {
		t.Fatal("generic POST transport errors must not be retried")
	}
	if retryLoopbackTransport(http.MethodPost, syscall.EPERM) {
		t.Fatal("permission-denied POST errors must not be retried")
	}
	if !retryLoopbackTransport(http.MethodGet, syscall.ECONNREFUSED) {
		t.Fatal("GET may retry a transient transport failure")
	}
	if retryLoopbackTransport(http.MethodGet, syscall.EPERM) {
		t.Fatal("permission-denied GET errors must not bypass sandbox policy")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestApiCallRaw_InjectedTransport_PostSendFailureNoDuplicateMutation(t *testing.T) {
	oldTransport := http.DefaultTransport
	oldURL := serverURL
	defer func() {
		http.DefaultTransport = oldTransport
		serverURL = oldURL
	}()

	serverURL = "http://localhost:8420"

	var callCount int
	var mutationCount int

	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		if req.URL.Host == "localhost:8420" {
			// Simulate server receiving and processing the mutating request
			if req.Body != nil {
				io.ReadAll(req.Body)
			}
			mutationCount++
			// Simulate post-send transport failure (e.g. read timeout or connection reset after server processed mutation)
			return nil, &net.OpError{
				Op:  "read",
				Net: "tcp",
				Err: io.ErrUnexpectedEOF,
			}
		}
		if req.URL.Host == "127.0.0.1:8420" {
			mutationCount++
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Header:     make(http.Header),
			}, nil
		}
		return nil, fmt.Errorf("unexpected host: %s", req.URL.Host)
	})

	// Perform mutating POST request (e.g. task complete)
	_, status, err := apiCallRaw(http.MethodPost, "/test-proj/tasks/1/complete", map[string]string{"outcome": "success"})

	// Assertions:
	// 1. Post-send failure must NOT be retried: callCount must be exactly 1.
	if callCount != 1 {
		t.Fatalf("expected exactly 1 call attempt, got %d (mutating POST was dangerously retried!)", callCount)
	}
	// 2. Server mutation must NOT be duplicated: mutationCount must remain 1.
	if mutationCount != 1 {
		t.Fatalf("duplicate mutation detected! mutationCount = %d, want 1", mutationCount)
	}
	// 3. Error must be returned to caller.
	if err == nil {
		t.Fatalf("expected error from failed post-send transport, got status %d", status)
	}
}

func TestApiCallRaw_InjectedTransport_PreConnectFailureRetriesGetOnly(t *testing.T) {
	oldTransport := http.DefaultTransport
	oldURL := serverURL
	defer func() {
		http.DefaultTransport = oldTransport
		serverURL = oldURL
	}()

	serverURL = "http://localhost:8420"

	var callCount int
	var hostsVisited []string

	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		hostsVisited = append(hostsVisited, req.URL.Host)
		if req.URL.Host == "localhost:8420" {
			// Pre-connect dial failure (e.g. connection refused on loopback)
			return nil, &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: syscall.ECONNREFUSED,
			}
		}
		if req.URL.Host == "127.0.0.1:8420" {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"status":"healthy"}`)),
				Header:     make(http.Header),
			}, nil
		}
		return nil, fmt.Errorf("unexpected host: %s", req.URL.Host)
	})

	// Idempotent GET request should safely retry to 127.0.0.1 on pre-connect transport failure
	respData, status, err := apiCallRaw(http.MethodGet, "/test-proj/subscribers", nil)
	if err != nil {
		t.Fatalf("expected successful GET retry to 127.0.0.1, got error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected status 200, got %d", status)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 call attempts (localhost followed by 127.0.0.1), got %d", callCount)
	}
	if len(hostsVisited) != 2 || hostsVisited[0] != "localhost:8420" || hostsVisited[1] != "127.0.0.1:8420" {
		t.Fatalf("unexpected hosts visited: %v", hostsVisited)
	}
	if !strings.Contains(string(respData), "healthy") {
		t.Fatalf("unexpected response body: %s", string(respData))
	}
}

func TestApiCallRaw_InjectedTransport_PermissionDeniedNeverRetried(t *testing.T) {
	oldTransport := http.DefaultTransport
	oldURL := serverURL
	defer func() {
		http.DefaultTransport = oldTransport
		serverURL = oldURL
	}()

	serverURL = "http://localhost:8420"

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			var callCount int
			http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				callCount++
				return nil, &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: syscall.EPERM,
				}
			})

			_, _, err := apiCallRaw(method, "/test-proj/tasks", map[string]string{"title": "test"})
			if err == nil {
				t.Fatalf("expected error from EPERM dial, got nil")
			}
			// Must never attempt alternate route to evade sandbox policy
			if callCount != 1 {
				t.Fatalf("permission denial must not trigger fallback retry (evading sandbox policy); callCount = %d, want 1", callCount)
			}
		})
	}
}
