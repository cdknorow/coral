package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/diskguard"
	"github.com/subgentic/coral/internal/store"
)

// #4602: with the data volume nearly full, the routes that can fill it refuse with 507 disk_low before writing; with room they work.
func lowDisk(t *testing.T, free uint64) {
	t.Cleanup(diskguard.UseForTest(func(string) (uint64, uint64, error) { return free, 100 << 30, nil }, time.Now))
}

func TestAgentArtifactUploadRefusesOnALowDisk(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "artifact-session", AgentName: "artifact-agent", AgentType: "codex", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	h.RegisterAgentArtifacts(r)
	server := httptest.NewServer(r)
	defer server.Close()
	upload := func() (int, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/artifacts?session_id=artifact-session", strings.NewReader("hello artifact"))
		require.NoError(t, err)
		req.Header.Set("X-Artifact-Name", "report.md")
		req.Header.Set("X-Artifact-Media-Type", "text/markdown")
		resp, err := agentClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	lowDisk(t, 200<<20)
	code, out := upload()
	assert.Equal(t, http.StatusInsufficientStorage, code)
	assert.Equal(t, "disk_low", out["code"])
	assert.EqualValues(t, 200<<20, out["free_bytes"])
	lowDisk(t, 50<<30)
	code, _ = upload()
	assert.Equal(t, http.StatusCreated, code, "with room it is stored")
}

func TestBoardPostRefusesOnALowDisk(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/lowdisk"
	post := func() (int, string) {
		resp := postJSON(t, base+"/messages", map[string]string{"subscriber_id": "sess-1", "content": "a message"})
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	lowDisk(t, 100<<20)
	code, body := post()
	assert.Equal(t, http.StatusInsufficientStorage, code)
	assert.Contains(t, body, "disk_low")
	lowDisk(t, 50<<30)
	code, _ = post()
	assert.NotEqual(t, http.StatusInsufficientStorage, code, "with room the post is not refused for the disk")
}

// #4602: a board post is at most 256 KiB; the limit passes, one byte over is 413 content_too_large and nothing is stored.
func TestBoardPostContentIsCappedAt256KiB(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/bigpost"
	post := func(n int) (int, string) {
		resp := postJSON(t, base+"/messages", map[string]string{"subscriber_id": "sess-1", "content": strings.Repeat("x", n)})
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, _ := post(maxBoardPostContent)
	assert.NotEqual(t, http.StatusRequestEntityTooLarge, code, "the limit itself is allowed")
	code, body := post(maxBoardPostContent + 1)
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Contains(t, body, "content_too_large")
	assert.Contains(t, body, "artifact")
}

// coral-board prints a refusal's "message" before its code (serverError): the 413 and the 507 carry one, the same
// words as "error", so an agent reads how to fix it and not only "content_too_large" or "disk_low" (#4629).
func TestBoardPostRefusalsCarryAMessageForTheCLI(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/refusals"
	refusal := func(content string) (int, map[string]any) {
		resp := postJSON(t, base+"/messages", map[string]string{"subscriber_id": "sess-1", "content": content})
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return resp.StatusCode, out
	}
	code, out := refusal(strings.Repeat("x", maxBoardPostContent+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.NotEmpty(t, out["message"])
	assert.Equal(t, out["error"], out["message"])
	assert.Contains(t, out["message"], "artifact", "it says what to do")

	lowDisk(t, 100<<20)
	code, out = refusal("a message")
	assert.Equal(t, http.StatusInsufficientStorage, code)
	assert.NotEmpty(t, out["message"])
	assert.Equal(t, out["error"], out["message"])
	assert.Contains(t, out["message"], "Free some space")
}
