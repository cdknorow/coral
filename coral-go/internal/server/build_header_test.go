package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3751: every /api/ answer names the server's static build, so a dashboard tab that outlived an update
// can tell its scripts are old. Other paths, and a server that has no build id, send nothing.
func TestAPIAnswersNameTheBuild(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	s := &Server{staticVersion: "0123456789ab"}
	for path, want := range map[string]string{
		"/api/health": "0123456789ab", "/api/sessions/live": "0123456789ab", "/api/system/status": "0123456789ab",
		"/": "", "/static/app.js": "", "/ws/coral": "",
	} {
		rec := httptest.NewRecorder()
		s.buildHeader(next).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		assert.Equal(t, want, rec.Header().Get("X-Coral-Build"), path)
	}
	rec := httptest.NewRecorder()
	(&Server{}).buildHeader(next).ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	_, has := rec.Header()["X-Coral-Build"]
	assert.False(t, has, "no header while the build is unknown")
}
