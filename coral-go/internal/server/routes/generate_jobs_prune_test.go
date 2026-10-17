package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

// #4167: a team-generation job's result is kept generateJobTTL and then pruned when jobs are used,
// not by a sweeper goroutine per SystemHandler that never ended.
func TestGenerateJobsArePrunedOnUse(t *testing.T) {
	h := &SystemHandler{generateJobs: map[string]*generateJob{
		"gen-fresh": {Status: "done", CreatedAt: time.Now()},
		"gen-old":   {Status: "done", CreatedAt: time.Now().Add(-generateJobTTL - time.Minute)},
	}}
	r := chi.NewRouter()
	r.Get("/api/teams/generate/{jobId}", h.GenerateTeamStatus)
	status := func(id string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/teams/generate/"+id, nil))
		return w.Code
	}
	assert.Equal(t, http.StatusOK, status("gen-fresh"))
	assert.Equal(t, http.StatusNotFound, status("gen-old"), "past its TTL: gone")
	h.generateJobsMu.Lock()
	_, kept := h.generateJobs["gen-old"]
	h.generateJobsMu.Unlock()
	assert.False(t, kept, "and pruned from the map")
}
