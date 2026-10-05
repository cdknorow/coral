package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestAgentArtifactUploadAndDownload(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "artifact-session", AgentName: "artifact-agent", AgentType: "codex", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	h.RegisterAgentArtifacts(r)
	server := httptest.NewServer(r)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/artifacts?session_id=artifact-session", strings.NewReader("hello artifact"))
	require.NoError(t, err)
	req.Header.Set("X-Artifact-Name", "report.md")
	req.Header.Set("X-Artifact-Media-Type", "text/markdown")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var uploaded struct {
		URI  string `json:"uri"`
		URL  string `json:"url"`
		Size int    `json:"size"`
	}
	require.NoError(t, json.Unmarshal(body, &uploaded))
	require.Equal(t, 14, uploaded.Size)
	require.True(t, strings.HasPrefix(uploaded.URI, "coral://artifacts/"))

	resp, err = http.Get(server.URL + uploaded.URL)
	require.NoError(t, err)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/markdown", resp.Header.Get("Content-Type"))
	require.Equal(t, "hello artifact", string(data))
}

func TestAgentArtifactUploadRecoversMarkdownType(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "md-session", AgentName: "md-agent", AgentType: "codex", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	h.RegisterAgentArtifacts(r)
	server := httptest.NewServer(r)
	defer server.Close()

	cases := []struct{ name, declared, want string }{
		{"audit.md", "", "text/markdown; charset=utf-8"},
		{"audit.md", "application/octet-stream", "text/markdown; charset=utf-8"},
		{"audit.md", "image/png", "image/png"},
		{"blob.zzunknown", "", "application/octet-stream"},
		{"blob", "application/octet-stream", "application/octet-stream"},
	}
	for i, c := range cases {
		payload := "# body " + string(rune('a'+i)) + "\x00\x01"
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/artifacts?session_id=md-session", strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("X-Artifact-Name", c.name)
		if c.declared != "" {
			req.Header.Set("X-Artifact-Media-Type", c.declared)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
		var up struct {
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
		}
		require.NoError(t, json.Unmarshal(body, &up))
		require.Equal(t, c.want, up.MediaType, "%+v", c)
		got, err := http.Get(server.URL + up.URL)
		require.NoError(t, err)
		data, _ := io.ReadAll(got.Body)
		got.Body.Close()
		require.Equal(t, c.want, got.Header.Get("Content-Type"))
		require.Equal(t, payload, string(data))
	}
}
