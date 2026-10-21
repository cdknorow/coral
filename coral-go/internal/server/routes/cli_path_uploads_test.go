package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/uploads"
)

func putSettingsJSON(t *testing.T, base string, body map[string]any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", base+"/api/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	msg, _ := out["error"].(string)
	return resp.StatusCode, msg
}

// #3364 (audit #3357 item 5): a CLI path is saved only if it's an absolute path to an
// executable file with no shell metacharacters, for a known agent type.
func TestPutSettingsValidatesCLIPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bits")
	}
	server, handler := setupSystemTestServer(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	plain := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(plain, []byte("x"), 0o644))
	saved := func(k string) string {
		s, err := handler.ss.GetSettings(context.Background())
		require.NoError(t, err)
		return s[k]
	}

	code, _ := putSettingsJSON(t, server.URL, map[string]any{"cli_path_claude": exe})
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, exe, saved("cli_path_claude"))

	for name, tc := range map[string]struct {
		key, value, msg string
	}{
		"relative":       {"cli_path_codex", "bin/codex", "must be absolute"},
		"missing":        {"cli_path_codex", filepath.Join(dir, "nope"), "no file at"},
		"not executable": {"cli_path_codex", plain, "not executable"},
		"a directory":    {"cli_path_codex", dir, "not a file"},
		"metachar":       {"cli_path_codex", exe + "; touch /tmp/x", "must not contain"},
		"substitution":   {"cli_path_codex", "$(touch /tmp/x)", "must not contain"},
		"newline":        {"cli_path_codex", exe + "\nid", "must not contain"},
		"unknown type":   {"cli_path_bash", exe, "unknown agent type"},
	} {
		code, msg := putSettingsJSON(t, server.URL, map[string]any{tc.key: tc.value, "default_working_dir": "/tmp/sentinel-" + name})
		assert.Equal(t, http.StatusBadRequest, code, name)
		assert.Contains(t, msg, tc.msg, name)
		assert.Empty(t, saved(tc.key), "%s: not saved", name)
		assert.NotEqual(t, "/tmp/sentinel-"+name, saved("default_working_dir"), "%s: nothing in the request is saved", name)
	}

	// A saved path whose CLI was removed since doesn't block saving other settings.
	require.NoError(t, os.Remove(exe))
	code, _ = putSettingsJSON(t, server.URL, map[string]any{"cli_path_claude": exe, "theme": "dark"})
	assert.Equal(t, http.StatusOK, code)
	// Clearing it is always allowed.
	code, _ = putSettingsJSON(t, server.URL, map[string]any{"cli_path_claude": ""})
	assert.Equal(t, http.StatusOK, code)
	assert.Empty(t, saved("cli_path_claude"))
}

func uploadPNG(t *testing.T, url string, size int) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "shot.png")
	require.NoError(t, err)
	fw.Write(make([]byte, size))
	mw.Close()
	resp, err := http.Post(url+"/api/upload", mw.FormDataContentType(), &body)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// #3364: the uploads folder has a size cap (oldest first, never the last hour) and an
// upload can be deleted by name.
func TestUploadCapAndDelete(t *testing.T) {
	coralDir := t.TempDir()
	prevDir, prevSettings := uploadDir, uploadSettings
	t.Cleanup(func() { uploadDir, uploadSettings = prevDir, prevSettings })
	InitUploadDir(coralDir)
	SetUploadSettings(func(context.Context) (map[string]string, error) {
		return map[string]string{uploads.MaxMBSetting: "50"}, nil
	})
	r := chi.NewRouter()
	r.Post("/api/upload", UploadFile)
	r.Delete("/api/upload/{name}", DeleteUpload)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// An old 20 MB upload, then fresh ones: the old one is evicted to make room.
	require.NoError(t, os.MkdirAll(uploadDir, 0o755))
	old := filepath.Join(uploadDir, "old.png")
	require.NoError(t, os.WriteFile(old, make([]byte, 20<<20), 0o644))
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(old, past, past))
	code, first := uploadPNG(t, srv.URL, 20<<20)
	require.Equal(t, http.StatusOK, code)
	code, _ = uploadPNG(t, srv.URL, 20<<20)
	require.Equal(t, http.StatusOK, code)
	assert.NoFileExists(t, old, "evicted for the cap")

	// Two fresh 20 MB uploads fill 50 MB: a third can't fit, the fresh ones are kept.
	code, out := uploadPNG(t, srv.URL, 20<<20)
	assert.Equal(t, http.StatusInsufficientStorage, code)
	assert.Contains(t, out["error"], "uploads folder is full")

	// Delete by name.
	name := filepath.Base(first["path"].(string))
	del := func(n string) int {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/upload/"+n, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, del(name))
	assert.NoFileExists(t, filepath.Join(uploadDir, name))
	assert.Equal(t, http.StatusNotFound, del(name))
	// Outside the folder: never deleted (refused by the router or the handler).
	outside := filepath.Join(coralDir, "sessions.db")
	require.NoError(t, os.WriteFile(outside, []byte("db"), 0o600))
	for _, n := range []string{"..%2Fsessions.db", "%2E%2E%2Fsessions.db", "..%5Csessions.db"} {
		assert.NotEqual(t, http.StatusOK, del(n), n)
	}
	assert.FileExists(t, outside)
}

// #3602 W28: a dashboard upload is this user's only: the folder 0700, the file 0600.
func TestUploadIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	coralDir := t.TempDir()
	prevDir, prevSettings := uploadDir, uploadSettings
	t.Cleanup(func() { uploadDir, uploadSettings = prevDir, prevSettings })
	InitUploadDir(coralDir)
	SetUploadSettings(func(context.Context) (map[string]string, error) { return map[string]string{}, nil })
	r := chi.NewRouter()
	r.Post("/api/upload", UploadFile)
	srv := httptest.NewServer(r)
	defer srv.Close()
	code, out := uploadPNG(t, srv.URL, 1<<10)
	require.Equal(t, http.StatusOK, code)
	fi, err := os.Stat(uploadDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "the uploads folder")
	fi, err = os.Stat(out["path"].(string))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "the uploaded file")
}

// #3602 W26: an upload folder's session id is one safe path element.
func TestUploadsDirTakesOnlySafeIDs(t *testing.T) {
	coralDir := t.TempDir()
	assert.Equal(t, filepath.Join(coralDir, "artifacts", "uploads", "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11"), uploadsDir(coralDir, "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11"))
	assert.NotEmpty(t, uploadsDir(coralDir, "sess-1"))
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "C:", "a:b", "a.b", "-x", "a b", strings.Repeat("a", 129)} {
		assert.Empty(t, uploadsDir(coralDir, bad), "%q", bad)
	}
}
