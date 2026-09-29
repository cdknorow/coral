package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkingModeCLI(t *testing.T) {
	oldURL := serverURL
	defer func() { serverURL = oldURL }()
	var requests []string
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != "GET" {
			var body map[string]any
			if r.ContentLength > 0 {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			}
			bodies = append(bodies, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"worktrees","dependency_guidance":true,"custom_instructions":"Keep me","instructions":"generated"}`))
	}))
	defer server.Close()
	serverURL = server.URL
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--mode", "custom"}))
	require.Equal(t, []string{"GET /api/board/team/working-mode", "PUT /api/board/team/working-mode"}, requests)
	require.Equal(t, map[string]any{"mode": "custom", "dependency_guidance": true, "custom_instructions": "Keep me"}, bodies[0])
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--custom-instructions=", "--dependency-guidance=false"}))
	require.Equal(t, "", bodies[1]["custom_instructions"])
	require.Equal(t, false, bodies[1]["dependency_guidance"])
	file := filepath.Join(t.TempDir(), "instructions.txt")
	require.NoError(t, os.WriteFile(file, []byte("Review carefully.\nRun checks."), 0600))
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--create", "review", "--name", "Review", "--instructions-file", file}))
	require.Equal(t, "POST /api/board/team/working-mode/presets", requests[len(requests)-1])
	require.Equal(t, "Review carefully.\nRun checks.", bodies[2]["instructions"])
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--edit", "none", "--instructions="}))
	require.Equal(t, "PUT /api/board/team/working-mode/presets/none", requests[len(requests)-1])
	require.Equal(t, "", bodies[3]["instructions"])
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--reset", "worktrees"}))
	require.Equal(t, "POST /api/board/team/working-mode/presets/worktrees/reset", requests[len(requests)-1])
	require.NoError(t, runWorkingMode([]string{"--board", "team", "--presets"}))
	require.Equal(t, "GET /api/board/team/working-mode/presets", requests[len(requests)-1])
	for _, args := range [][]string{
		{"--create", "new"}, {"--instructions", "x"}, {"--edit", "none", "--reset", "none"}, {"--presets", "--mode", "none"},
		{"--create", "new", "--instructions", "x", "--instructions-file", file},
	} {
		require.Error(t, runWorkingMode(append([]string{"--board", "team"}, args...)))
	}
}

func TestWorkingModeCLIServerError(t *testing.T) {
	oldURL := serverURL
	defer func() { serverURL = oldURL }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "already exists", 409) }))
	defer server.Close()
	serverURL = server.URL
	require.ErrorContains(t, runWorkingMode([]string{"--board", "team", "--create", "review", "--name", "Review", "--instructions", "X"}), "HTTP 409")
}
