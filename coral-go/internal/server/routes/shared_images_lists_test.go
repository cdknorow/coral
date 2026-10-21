package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #5284 (Security's review): with the owner's switch off, a request through the Hub tunnel must not learn a pasted image's name, id or size from an artifact list either, nor read its comments.
func TestSwitchOffHidesPastedImagesFromTheHubsListsToo(t *testing.T) {
	_, h, coralDir := teamArtifactServer(t)
	sid := "33333333-cccc-0000-0000-000000000003"
	subscribeSession(t, h, "alpha", "Carol", sid)
	pasted, _, err := storeArtifactBy(coralDir, sid, "Screenshot private.png", "image/png", uploadedByOwner, sourcePaste, cleanPNG(t))
	require.NoError(t, err)
	own, _, err := storeArtifact(coralDir, sid, "agent-made.md", "text/markdown", []byte("# an agent's own file\n"))
	require.NoError(t, err)

	share := "false"
	old := uploadSettings
	uploadSettings = func(context.Context) (map[string]string, error) {
		return map[string]string{SharePastedImagesSetting: share}, nil
	}
	t.Cleanup(func() { uploadSettings = old })

	list := func(tunneled bool) string {
		req := httptest.NewRequest(http.MethodGet, "/api/session-artifacts?session_id="+sid, nil)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "x")
		}
		rec := httptest.NewRecorder()
		h.ListSessionArtifacts(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}
	assert.NotContains(t, list(true), pasted, "off: the Hub's list has no pasted image")
	assert.NotContains(t, list(true), "Screenshot private.png")
	assert.Contains(t, list(true), own, "an agent's own upload is listed")
	assert.Contains(t, list(false), pasted, "off: this computer still lists it")
	share = "true"
	assert.Contains(t, list(true), pasted, "on: the Hub's list has it")
	// An unreadable setting is closed.
	uploadSettings = func(context.Context) (map[string]string, error) { return nil, assert.AnError }
	assert.NotContains(t, list(true), pasted)
}

func TestSwitchOffHidesAPastedImagesCommentsFromTheHub(t *testing.T) {
	rig := uploadRig(t)
	_, out := postImage(t, "p.png", cleanPNG(t), "s1")
	id := artifactOf(out)["id"].(string)
	// The comments read, behind the same stand-in dashboard caller as the rig.
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
		})
	})
	r.Get("/api/artifacts/{id}/comments", rig.h.listArtifactComments)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	get := func(tunneled bool) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/artifacts/"+id+"/comments", nil)
		require.NoError(t, err)
		req.Header.Set(testDashboardHeader, "1")
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
			req.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	local := get(false)
	require.NotEqual(t, http.StatusNotFound, local, "this computer reads the comments (status %d)", local)
	require.NoError(t, rig.ss.SetSetting(context.Background(), SharePastedImagesSetting, "false"))
	assert.Equal(t, http.StatusNotFound, get(true), "off: the Hub's read of the comments is 404")
	assert.Equal(t, local, get(false), "off: this computer still reads them")
}
