package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

func scheduleTestServer(t *testing.T) (*httptest.Server, *store.ScheduleStore) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sched.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewScheduleHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Post("/api/scheduled/jobs", h.CreateJob)
	r.Get("/api/scheduled/jobs/{jobID}", h.GetJob)
	r.Put("/api/scheduled/jobs/{jobID}", h.UpdateJob)
	r.Delete("/api/scheduled/jobs/{jobID}", h.DeleteJob)
	r.Post("/api/scheduled/jobs/{jobID}/toggle", h.ToggleJob)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, store.NewScheduleStore(db)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", dir).Run())
	return dir
}

func sendJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url, strings.NewReader(string(b)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// A job that could never run is refused when saved: an unknown IANA timezone,
// or a repo_path that is missing, not a directory, or not a git work tree.
// The same checks apply on edit (#3171).
func TestScheduledJobValidatesTimezoneAndRepo(t *testing.T) {
	srv, _ := scheduleTestServer(t)
	repo := gitRepo(t)
	plain := t.TempDir() // a directory, not a repository
	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	job := func(tz, repoPath string) map[string]any {
		return map[string]any{"name": "nightly", "cron_expr": "0 9 * * 1-5", "timezone": tz, "repo_path": repoPath, "prompt": "do it"}
	}
	for _, tc := range []struct {
		tz, repo, want string
	}{
		{"Mars/Olympus", repo, "unknown timezone: Mars/Olympus"},
		{"Europe/Berlin", "/definitely/not/here", "repo_path is not an existing directory"},
		{"Europe/Berlin", file, "repo_path is not an existing directory"},
		{"Europe/Berlin", plain, "repo_path is not a git repository"},
	} {
		code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job(tc.tz, tc.repo))
		assert.Equal(t, http.StatusBadRequest, code, tc)
		assert.Contains(t, body["error"], tc.want, tc)
	}
	for _, tz := range []string{"", "UTC", "Local", "America/New_York"} {
		code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job(tz, repo))
		assert.Equal(t, http.StatusOK, code, "timezone %q: %v", tz, body)
	}
	code, created := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job("Europe/Berlin", repo))
	require.Equal(t, http.StatusOK, code)
	id := strconv.Itoa(int(created["id"].(float64)))
	code, body := sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"timezone": "Not/AZone"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body["error"], "unknown timezone")
	code, body = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"repo_path": plain})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body["error"], "not a git repository")
	code, _ = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"timezone": "Asia/Tokyo"})
	assert.Equal(t, http.StatusOK, code)
}

// paused_reason is the scheduler's: a client cannot set it, and resuming a job
// (toggle or enabled:1) clears it (#3171).
func TestScheduledJobPausedReasonIsClearedOnResume(t *testing.T) {
	srv, ss := scheduleTestServer(t)
	repo := gitRepo(t)
	code, created := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", map[string]any{"name": "j", "cron_expr": "0 9 * * *", "repo_path": repo, "prompt": "p"})
	require.Equal(t, http.StatusOK, code)
	id := int64(created["id"].(float64))
	ctx := context.Background()
	pause := func() {
		_, err := ss.UpdateScheduledJob(ctx, id, map[string]interface{}{"enabled": 0, "paused_reason": "Paused after 3 failed runs in a row: boom"})
		require.NoError(t, err)
	}
	reason := func() string {
		j, err := ss.GetScheduledJob(ctx, id)
		require.NoError(t, err)
		return j.PausedReason
	}

	_, _ = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+strconv.FormatInt(id, 10), map[string]any{"paused_reason": "set by a client"})
	assert.Equal(t, "", reason(), "a client cannot set paused_reason")

	pause()
	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs/"+strconv.FormatInt(id, 10)+"/toggle", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, float64(1), body["enabled"])
	assert.Equal(t, "", reason(), "toggling it back on clears the reason")

	pause()
	code, _ = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+strconv.FormatInt(id, 10), map[string]any{"enabled": 1})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", reason(), "enabled:1 clears the reason")
}

// TestGetJobUnknownOrDeletedAnswers404 verifies that GET of an unknown or deleted
// job ID answers 404 with error JSON, not 200 with body null.
func TestGetJobUnknownOrDeletedAnswers404(t *testing.T) {
	srv, _ := scheduleTestServer(t)
	repo := gitRepo(t)

	// 1. Unknown job ID answers 404
	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/scheduled/jobs/999999", nil)
	assert.Equal(t, http.StatusNotFound, code, "unknown job answers 404")
	assert.Equal(t, "job not found", body["error"])

	// 2. Create job
	createCode, created := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", map[string]any{
		"name":      "test-job",
		"cron_expr": "0 9 * * *",
		"repo_path": repo,
		"prompt":    "run test",
	})
	require.Equal(t, http.StatusOK, createCode)
	id := int64(created["id"].(float64))
	idStr := strconv.FormatInt(id, 10)

	// 3. Existing job answers 200
	code, got := sendJSON(t, http.MethodGet, srv.URL+"/api/scheduled/jobs/"+idStr, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "test-job", got["name"])

	// 4. Delete job
	delCode, _ := sendJSON(t, http.MethodDelete, srv.URL+"/api/scheduled/jobs/"+idStr, nil)
	require.Equal(t, http.StatusOK, delCode)

	// 5. Deleted job answers 404, not 200 with null
	code, deletedBody := sendJSON(t, http.MethodGet, srv.URL+"/api/scheduled/jobs/"+idStr, nil)
	assert.Equal(t, http.StatusNotFound, code, "deleted job answers 404")
	assert.Equal(t, "job not found", deletedBody["error"])
}
