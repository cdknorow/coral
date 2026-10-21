package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
)

func getKnowledgeStatus(t *testing.T, team string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/teams/detail/"+team+"/distill-knowledge", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", team)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	(&SessionsHandler{}).GetKnowledgeStatus(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m))
	return m
}

// #3325: the status says when the run started and finished, so a reopened dashboard can
// show "Updated <time>"; a team with no run (or after a restart) is 'none'.
func TestKnowledgeStatusCarriesTimes(t *testing.T) {
	assert.Equal(t, "none", getKnowledgeStatus(t, "no-such-team-3325")["status"])

	knowledgeJobsMu.Lock()
	knowledgeJobs["team-3325"] = &knowledgeJob{TeamName: "team-3325", Status: "complete",
		StartedAt: "2026-10-14T06:00:00Z", FinishedAt: "2026-10-14T06:05:00Z"}
	knowledgeJobsMu.Unlock()
	t.Cleanup(func() { knowledgeJobsMu.Lock(); delete(knowledgeJobs, "team-3325"); knowledgeJobsMu.Unlock() })

	m := getKnowledgeStatus(t, "team-3325")
	assert.Equal(t, "complete", m["status"])
	assert.Equal(t, "2026-10-14T06:00:00Z", m["started_at"])
	assert.Equal(t, "2026-10-14T06:05:00Z", m["finished_at"])
}

// The run's goroutine updates the job under the lock while dashboards poll it: the status
// is written from a copy taken under the lock (go test -race).
func TestKnowledgeStatusIsRaceFreeWhileTheRunUpdatesIt(t *testing.T) {
	job := &knowledgeJob{TeamName: "race-3325", Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	knowledgeJobsMu.Lock()
	knowledgeJobs["race-3325"] = job
	knowledgeJobsMu.Unlock()
	t.Cleanup(func() { knowledgeJobsMu.Lock(); delete(knowledgeJobs, "race-3325"); knowledgeJobsMu.Unlock() })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			knowledgeJobsMu.Lock()
			job.Progress = background.DistillProgress{Phase: "extracting", Agent: "a", Total: 200, Completed: i}
			knowledgeJobsMu.Unlock()
		}
	}()
	for i := 0; i < 200; i++ {
		assert.Equal(t, "running", getKnowledgeStatus(t, "race-3325")["status"])
	}
	wg.Wait()
}
