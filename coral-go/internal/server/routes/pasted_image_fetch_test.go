package routes

import (
	"bytes"
	"context"
	"image"
	_ "image/png" // the stand-in agent decodes what it fetched
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// A pasted image reaches the agent as its coral://artifacts link alone (#5481), so the agent has to be able to fetch it: the stand-in of each agent type (Claude, Codex, Antigravity, Pi), acting
// as its own session with its token, gets the image from the artifact route that `coral-agent artifact download` calls, byte for byte the file the dashboard stored, and can decode it.
func TestEveryAgentTypeCanFetchAPastedImageByItsLink(t *testing.T) {
	rig := uploadRig(t)
	pasted := cleanPNG(t)
	code, out := postImage(t, "screenshot_20261020_101500_ab12.png", pasted, "s1")
	require.Equal(t, http.StatusOK, code, out)
	art := artifactOf(out)
	require.NotNil(t, art, out)
	id := art["id"].(string)
	file, err := os.ReadFile(out["path"].(string))
	require.NoError(t, err)

	for i, agentType := range []string{"claude", "codex", "agy", "antigravity", "gemini", "pi"} {
		sid := "fetch-" + agentType
		require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "stand-in-" + agentType, AgentType: agentType, WorkingDir: t.TempDir()}), "agent %d", i)
		req, err := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/artifacts/"+id, nil)
		require.NoError(t, err)
		req.Header.Set(testAgentHeader, sid) // as that session's agent, with its token
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", agentType, body)
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"), agentType)
		assert.Equal(t, file, body, "%s gets the very bytes the dashboard stored", agentType)
		cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
		require.NoError(t, err, "%s can read what it fetched", agentType)
		assert.Equal(t, "png", format)
		assert.Equal(t, 2, cfg.Width)
	}
}
