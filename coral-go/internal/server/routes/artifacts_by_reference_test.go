package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #4379 T3: there is no upload "by reference". A request naming an existing object (a header, a
// JSON body with its link or digest) never gives the caller an upload record of THAT object: what
// is stored is the request's own bytes, under their own id; an empty body is the empty object.
func TestUploadByReferenceRecordsOnlyTheRequestsOwnBytes(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	for _, sid := range []string{"owner-session", "other-session"} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: sid, AgentType: "codex", WorkingDir: t.TempDir()}))
	}
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	h.RegisterAgentArtifacts(r)
	server := httptest.NewServer(r)
	defer server.Close()
	upload := func(sid, body string, headers map[string]string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/artifacts?session_id="+sid, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Artifact-Name", "f.md")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := agentClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out.URI
	}
	code, owned := upload("owner-session", "the owner's bytes", nil)
	require.Equal(t, http.StatusCreated, code)
	id := managedArtifactID(owned)
	require.NotEmpty(t, id)

	sum := sha256.Sum256(nil)
	empty := hex.EncodeToString(sum[:])
	attempts := []struct {
		body    string
		headers map[string]string
	}{
		{"", map[string]string{"X-Artifact-Id": id, "X-Artifact-Digest": "sha256:" + id, "X-Artifact-Uri": owned}},
		{`{"uri":"` + owned + `"}`, map[string]string{"Content-Type": "application/json"}},
		{`{"digest":"sha256:` + id + `"}`, map[string]string{"Content-Type": "application/json"}},
	}
	for _, a := range attempts {
		code, got := upload("other-session", a.body, a.headers)
		if code == http.StatusCreated {
			assert.NotEqual(t, id, managedArtifactID(got), "stored as its own object, never the one it names")
			if a.body == "" {
				assert.Equal(t, empty, managedArtifactID(got), "an empty body is the empty object")
			}
		}
	}
	_, err := os.Stat(filepath.Join(uploadsDir(h.cfg.CoralDir(), "other-session"), id+".json"))
	assert.True(t, os.IsNotExist(err), "no upload record of the named object for the other session")
}
