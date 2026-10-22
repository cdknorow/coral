package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func artifactServer(t *testing.T, data []byte) (*httptest.Server, string) {
	t.Helper()
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/artifacts/"+id {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, id
}

// An existing --output file is named in the error, with what to do about it (it used to be the raw "link ...: Cannot create a file when that
// file already exists."), and --force replaces it (#222-E).
func TestDownloadToAnExistingFileSaysSoAndForceReplacesIt(t *testing.T) {
	srv, id := artifactServer(t, []byte("new content"))
	out := filepath.Join(t.TempDir(), "got.txt")
	require.NoError(t, os.WriteFile(out, []byte("old"), 0o600))

	_, err := downloadArtifactOpts(srv.URL, id, out, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--force")
	assert.Contains(t, err.Error(), out)
	assert.NotContains(t, err.Error(), "link ")
	b, _ := os.ReadFile(out)
	assert.Equal(t, "old", string(b), "nothing was replaced without --force")

	p, err := downloadArtifactOpts(srv.URL, id, out, true)
	require.NoError(t, err)
	assert.Equal(t, out, p)
	b, _ = os.ReadFile(out)
	assert.Equal(t, "new content", string(b))
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), "coral-artifact-*"))
	assert.Empty(t, left, "no temporary file is left beside it")

	fresh := filepath.Join(t.TempDir(), "fresh.txt")
	_, err = downloadArtifactOpts(srv.URL, id, fresh, true)
	require.NoError(t, err, "--force with no file there is a plain download")
}

// splitOptions: options stand before, between or after the positional arguments, in any of the flag package's spellings.
func TestSplitOptionsTakesOptionsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"--output", "f", "link"}, {"-output", "f", "link"}, {"link", "--output", "f"}, {"link", "-output=f"}, {"--output=f", "link"},
	} {
		got, err := splitOptions(args, []string{"output"}, []string{"force"})
		require.NoError(t, err, "%v", args)
		assert.Equal(t, []string{"link"}, got.Positional, "%v", args)
		assert.Equal(t, "f", got.Values["output"], "%v", args)
	}
	got, err := splitOptions([]string{"link", "--force"}, []string{"output"}, []string{"force"})
	require.NoError(t, err)
	assert.True(t, got.Bools["force"])
	for _, args := range [][]string{{"link", "--nope"}, {"link", "--output"}, {"link", "--force=1"}} {
		_, err := splitOptions(args, []string{"output"}, []string{"force"})
		assert.Error(t, err, "%v", args)
	}
	got, err = splitOptions([]string{"--", "-odd-name"}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"-odd-name"}, got.Positional, "after -- everything is positional")
}

// The command: a flag before the link works (it was read as the link), an unknown one is refused with exit 2.
func TestArtifactDownloadCommandOptionOrder(t *testing.T) {
	srv, id := artifactServer(t, []byte("payload"))
	out := filepath.Join(t.TempDir(), "a.txt")
	for _, args := range [][]string{
		{"artifact", "download", "--output", out, "coral://artifacts/" + id},
		{"artifact", "download", "coral://artifacts/" + id, "--output", out + "2"},
	} {
		code, stdout, stderr := runAgent(t, srv.URL, args...)
		require.Equal(t, 0, code, "%v: %s", args, stderr)
		got, err := os.ReadFile(strings.TrimSpace(stdout))
		require.NoError(t, err)
		assert.Equal(t, "payload", string(got))
	}
	code, _, stderr := runAgent(t, srv.URL, "artifact", "download", "coral://artifacts/"+id, "--nope")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "unknown option")
	code, _, stderr = runAgent(t, srv.URL, "artifact", "download", "--output", out, "coral://artifacts/"+id)
	assert.Equal(t, 1, code, "an existing file")
	assert.Contains(t, stderr, "--force")
	code, _, _ = runAgent(t, srv.URL, "artifact", "download", "--force", "--output", out, "coral://artifacts/"+id)
	assert.Equal(t, 0, code)
}

func TestTaskListRefusesUnknownOptionsAndHasJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": 3, "status": "pending", "priority": "high", "title": "t"}}})
	}))
	defer srv.Close()
	for _, args := range [][]string{{"task", "list", "--not-a-flag"}, {"task", "current", "--x"}, {"task", "detail", "1", "--x"}, {"task", "detail", "1", "2"}} {
		code, _, stderr := runAgent(t, srv.URL, args...)
		assert.Equal(t, 2, code, "%v: %s", args, stderr)
		assert.Contains(t, stderr, "Usage", "%v", args)
	}
	code, stdout, stderr := runAgent(t, srv.URL, "task", "list", "--json")
	require.Equal(t, 0, code, stderr)
	var tasks []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &tasks), stdout)
	require.Len(t, tasks, 1)
	assert.EqualValues(t, 3, tasks[0]["id"])
	code, stdout, _ = runAgent(t, srv.URL, "task", "list")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "ID", "the table is still the default")
}
