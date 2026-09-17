package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

const popoutTestSessionID = "abcdef11-2222-4333-8444-555555555555"

func newPopoutRouteTestServer(t *testing.T) *Server {
	t.Helper()
	tmpl, err := template.ParseFS(templateFS,
		"frontend/templates/index.html",
		"frontend/templates/includes/sidebar.html",
		"frontend/templates/includes/modals.html",
		"frontend/templates/includes/views/live_session.html",
		"frontend/templates/includes/views/history_session.html",
		"frontend/templates/includes/views/message_board.html",
		"frontend/templates/includes/views/workflows.html",
		"frontend/templates/includes/views/connected_apps.html",
		"frontend/templates/includes/views/docs.html",
		"frontend/templates/includes/views/cost_dashboard.html",
	)
	require.NoError(t, err)
	return &Server{cfg: &config.Config{CoralRoot: "/tmp/coral"}, indexTmpl: tmpl}
}

func TestAgentPopoutRouteRendersCanonicalBootData(t *testing.T) {
	s := newPopoutRouteTestServer(t)
	r := chi.NewRouter()
	r.Get("/agent/{sessionID}", s.serveAgentPopout)

	req := httptest.NewRequest(http.MethodGet, "/agent/"+popoutTestSessionID, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, `data-entry-mode="agent"`)
	assert.Contains(t, body, `data-target-session-id="`+popoutTestSessionID+`"`)
}

func TestAgentPopoutRouteRejectsMalformedOrNonCanonicalUUID(t *testing.T) {
	s := newPopoutRouteTestServer(t)
	r := chi.NewRouter()
	r.Get("/agent/{sessionID}", s.serveAgentPopout)

	for _, id := range []string{"not-a-uuid", strings.ToUpper(popoutTestSessionID)} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/agent/"+id, nil))
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	}
}

func TestDashboardTemplateHasEmptyPopoutBootData(t *testing.T) {
	s := newPopoutRouteTestServer(t)
	rr := httptest.NewRecorder()
	s.indexTmpl.Execute(rr, templateData{CoralRoot: `"><script>alert(1)</script>`})
	body := rr.Body.String()
	assert.Contains(t, body, `data-entry-mode="" data-target-session-id=""`)
	assert.NotContains(t, body, `<script>alert(1)</script>`)
}
