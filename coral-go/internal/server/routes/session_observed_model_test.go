package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

const observedModelSID = "00000000-0000-0000-0000-0000000000f2"

func postModel(t *testing.T, agentType, launchModel, body string) (*httptest.ResponseRecorder, *store.LiveSession) {
	t.Helper()
	_, h, _, ss := setupSessionsTestServer(t)
	ls := &store.LiveSession{SessionID: observedModelSID, AgentType: agentType, AgentName: "coral-go", WorkingDir: "/repo"}
	if launchModel != "" {
		ls.Model = &launchModel
	}
	require.NoError(t, ss.RegisterLiveSession(context.Background(), ls))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	r.Post("/api/sessions/live/{name}/context-window", h.UpdateContextWindow)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/coral-go/context-window", strings.NewReader(body))
	req.Header.Set(testAgentHeader, observedModelSID)
	r.ServeHTTP(rec, req)
	got, err := ss.GetLiveSession(context.Background(), observedModelSID)
	require.NoError(t, err)
	return rec, got
}

// #4588: agy's hook reports the model it runs; Coral keeps it for display and leaves the
// launch model (which a restart passes back to the CLI) alone, even for a model it knows.
func TestUpdateContextWindow_AgyModelIsObservedOnly(t *testing.T) {
	for _, model := range []string{"gemini-3.8-flash-high", "gemini-2.5-pro"} {
		rec, got := postModel(t, "agy", "gemini-3.5-pro", `{"session_id":"`+observedModelSID+`","model":"`+model+`"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, got.ObservedModel)
		assert.Equal(t, model, *got.ObservedModel)
		require.NotNil(t, got.Model)
		assert.Equal(t, "gemini-3.5-pro", *got.Model, "the launch model is untouched")
		assert.Zero(t, got.ContextWindow)
	}
}

func TestUpdateContextWindow_AgyModelThatIsNotAnIdIsRefused(t *testing.T) {
	for _, bad := range []string{"two words", "<synthetic>", "a\\nb", strings.Repeat("m", 200)} {
		rec, got := postModel(t, "agy", "", `{"session_id":"`+observedModelSID+`","model":"`+bad+`"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, bad)
		assert.Nil(t, got.ObservedModel, bad)
	}
}

// Claude's SessionStart report still sets the session's model and context window.
func TestUpdateContextWindow_ClaudeStillSetsTheModel(t *testing.T) {
	rec, got := postModel(t, "claude", "", `{"session_id":"`+observedModelSID+`","model":"claude-fable-5-1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, got.Model)
	assert.Equal(t, "claude-fable-5-1", *got.Model)
	assert.Nil(t, got.ObservedModel)
}

// The feed's model: the transcript's, else the hook's, else the launch's.
func TestAddTranscriptModel_HookModelBeatsLaunchAndLosesToTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-00000000c066"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), nil)
	hook := "gemini-3.8-flash-high"

	entry := map[string]any{"model": "launch-model"}
	h.addTranscriptModel(entry, sid, &hook)
	assert.Equal(t, map[string]any{"model": hook, "model_source": "hook"}, entry)

	empty := ""
	entry = map[string]any{"model": "launch-model"}
	h.addTranscriptModel(entry, sid, &empty)
	assert.Equal(t, "launch", entry["model_source"], "an empty observed model is no model")

	require.NoError(t, os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","type":"assistant","message":{"model":"claude-sonnet-5-5","content":[]},"timestamp":"2026-10-16T00:00:00Z"}`+"\n"), 0o600))
	h.jsonl.ReadClaudeTurnEvent(sid, "")
	h.addTranscriptModel(entry, sid, &hook)
	assert.Equal(t, "claude-sonnet-5-5", entry["model"])
	assert.Equal(t, "transcript", entry["model_source"])
}
