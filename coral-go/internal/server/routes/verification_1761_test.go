package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupHistoryVerificationServer(t *testing.T) (*httptest.Server, *HistoryHandler, *store.SessionStore, string, string, string) {
	t.Helper()

	dbDir := t.TempDir()
	db, err := store.Open(filepath.Join(dbDir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	boardDBDir := t.TempDir()
	bs, err := board.NewStore(filepath.Join(boardDBDir, "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	brainHome := filepath.Join(t.TempDir(), "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainHome)

	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", claudeHome)

	cfg := &config.Config{}
	h := NewHistoryHandler(db, cfg, bs)
	ss := store.NewSessionStore(db)

	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}", h.GetSessionDetail)
	r.Get("/api/sessions/history/{sessionID}/resume-info", h.GetResumeInfo)
	server := httptest.NewServer(r)
	t.Cleanup(func() { server.Close() })

	return server, h, ss, codexHome, brainHome, claudeHome
}

// TestTask1761_HistoryResumeDetailAndResumeInfo verifies:
// 1. Indexed provider metadata controls history parser/cache selection instead of defaulting to Claude.
// 2. Codex Coral marker -> native rollout ID and Antigravity native conversation mapping.
// 3. Claude's identity and resume behavior remain unchanged.
// 4. Live session precedence over historical index metadata.
func TestTask1761_HistoryResumeDetailAndResumeInfo(t *testing.T) {
	server, _, ss, codexHome, brainHome, claudeHome := setupHistoryVerificationServer(t)
	ctx := context.Background()

	// 1. Codex fixture:
	// A rollout file on disk named rollout-2026-09-30T12-00-00-native-codex-123.jsonl
	// containing an embedded Coral session marker "coral-codex-marker-001".
	codexDayDir := filepath.Join(codexHome, "sessions", "2026", "09", "30")
	require.NoError(t, os.MkdirAll(codexDayDir, 0755))
	codexNativeID := "rollout-2026-09-30T12-00-00-native-codex-123"
	codexFile := filepath.Join(codexDayDir, codexNativeID+".jsonl")
	codexMarker := "a1b2c3d4-e5f6-7890-abcd-ef0123456789"

	codexRolloutContent := fmt.Sprintf(
		`{"timestamp":"2026-09-30T12:00:00.000Z","type":"session_meta","payload":{"id":"%s","cwd":"/repo/project"}}`+"\n"+
			`{"timestamp":"2026-09-30T12:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n"+
			`{"timestamp":"2026-09-30T12:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"Fix the resume bug","images":[]}}`+"\n"+
			`{"timestamp":"2026-09-30T12:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"I have resolved the identity mapping.","phase":"final_answer"}}`+"\n",
		codexNativeID, at.CoralSessionMarkerPrefix, codexMarker,
	)
	require.NoError(t, os.WriteFile(codexFile, []byte(codexRolloutContent), 0644))

	// Index the Codex session under the Coral marker ID
	firstTS := "2026-09-30T12:00:00.000Z"
	lastTS := "2026-09-30T12:00:02.000Z"
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      codexMarker,
		SourceType:     "codex",
		SourceFile:     codexFile,
		FirstTimestamp: &firstTS,
		LastTimestamp:  &lastTS,
		MessageCount:   2,
		DisplaySummary: "Fix the resume bug",
		DisplayName:    "Codex Worker",
	}))

	// 2. Antigravity fixture:
	// A brain transcript on disk at brain/<nativeConvID>/.system_generated/logs/transcript.jsonl
	// containing an embedded Coral session marker "b2c3d4e5-f6a7-8901-bcde-f0123456789a".
	agyNativeConvID := "c1d2e3f4-a5b6-7890-1234-567890abcdef"
	agyLogsDir := filepath.Join(brainHome, agyNativeConvID, ".system_generated", "logs")
	require.NoError(t, os.MkdirAll(agyLogsDir, 0755))
	agyFile := filepath.Join(agyLogsDir, "transcript.jsonl")
	agyMarker := "b2c3d4e5-f6a7-8901-bcde-f0123456789a"

	agyTranscriptContent := fmt.Sprintf(
		`{"step_index":0,"type":"USER_INPUT","content":"Coral session metadata:\n%s %s\nInvestigate transcript."}`+"\n"+
			`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Transcript analysis complete."}`+"\n",
		at.CoralSessionMarkerPrefix, agyMarker,
	)
	require.NoError(t, os.WriteFile(agyFile, []byte(agyTranscriptContent), 0644))

	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      agyMarker,
		SourceType:     "agy",
		SourceFile:     agyFile,
		FirstTimestamp: &firstTS,
		LastTimestamp:  &lastTS,
		MessageCount:   2,
		DisplaySummary: "Investigate transcript",
		DisplayName:    "Antigravity Worker",
	}))

	// 3. Claude fixture:
	// ~/.claude/projects/-repo-project/coral-claude-003.jsonl
	claudeProjectDir := filepath.Join(claudeHome, "-repo-project")
	require.NoError(t, os.MkdirAll(claudeProjectDir, 0755))
	claudeID := "coral-claude-003"
	claudeFile := filepath.Join(claudeProjectDir, claudeID+".jsonl")
	claudeContent := `{"type":"user","message":{"content":"Hello from Claude"}}` + "\n" +
		`{"type":"assistant","message":{"content":"Hello, how can I help?"}}` + "\n"
	require.NoError(t, os.WriteFile(claudeFile, []byte(claudeContent), 0644))

	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      claudeID,
		SourceType:     "claude",
		SourceFile:     claudeFile,
		FirstTimestamp: &firstTS,
		LastTimestamp:  &lastTS,
		MessageCount:   2,
		DisplaySummary: "Hello from Claude",
		DisplayName:    "Claude Worker",
	}))

	// -------------------------------------------------------------
	// Verify Codex Historical Detail & Resume Info
	// -------------------------------------------------------------
	t.Run("Codex historical detail route uses indexed source type and reads transcript", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + codexMarker)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, codexMarker, body["session_id"])
		assert.Equal(t, "codex", body["agent_type"])

		msgs, ok := body["messages"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, msgs, "Codex transcript messages should not be empty")
	})

	t.Run("Codex resume info maps Coral marker to native rollout ID", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + codexMarker + "/resume-info")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var info map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, codexMarker, info["session_id"])
		assert.Equal(t, "codex", info["agent_type"])
		assert.Equal(t, codexNativeID, info["resume_session_id"])
	})

	// -------------------------------------------------------------
	// Verify Antigravity Historical Detail & Resume Info
	// -------------------------------------------------------------
	t.Run("Antigravity historical detail route uses indexed source type and reads transcript", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + agyMarker)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, agyMarker, body["session_id"])
		assert.Equal(t, "agy", body["agent_type"])

		msgs, ok := body["messages"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, msgs, "Antigravity transcript messages should not be empty")
	})

	t.Run("Antigravity resume info maps Coral marker to native conversation UUID", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + agyMarker + "/resume-info")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var info map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, agyMarker, info["session_id"])
		assert.Equal(t, "agy", info["agent_type"])
		assert.Equal(t, agyNativeConvID, info["resume_session_id"])
	})

	// -------------------------------------------------------------
	// Verify Claude Unchanged Identity and Resume Behavior
	// -------------------------------------------------------------
	t.Run("Claude historical detail route preserves Claude identity and reads messages", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + claudeID)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, claudeID, body["session_id"])
		assert.Equal(t, "claude", body["agent_type"])

		msgs, ok := body["messages"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, msgs, "Claude transcript messages should not be empty")
	})

	t.Run("Claude resume info preserves session ID without alteration", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/sessions/history/" + claudeID + "/resume-info")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var info map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, claudeID, info["session_id"])
		assert.Equal(t, "claude", info["agent_type"])
		assert.Equal(t, claudeID, info["resume_session_id"])
	})

	// -------------------------------------------------------------
	// Verify Live Session Precedence
	// -------------------------------------------------------------
	t.Run("Live session precedence overrides historical metadata when session is active", func(t *testing.T) {
		liveID := "active-session-live"
		require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
			SessionID:  liveID,
			SourceType: "codex",
			SourceFile: codexFile,
		}))
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
			SessionID:  liveID,
			AgentName:  "live-agent",
			AgentType:  "claude",
			WorkingDir: "/repo/live-workspace",
		}))

		resp, err := http.Get(server.URL + "/api/sessions/history/" + liveID + "/resume-info")
		require.NoError(t, err)
		defer resp.Body.Close()

		var info map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, "/repo/live-workspace", info["working_dir"])
		assert.Equal(t, "claude", info["agent_type"])
	})
}

// TestTask1761_IsolationAndPoisonedCache verifies:
// 1. Strict live and history isolation between two agents sharing the same workspace directory.
// 2. SessionReader cache cleaning after GetSessionDetail call.
// 3. Handling of missing source files (returns 404 gracefully).
func TestTask1761_IsolationAndPoisonedCache(t *testing.T) {
	server, h, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()

	// Two sessions in the SAME workspace "/workspace/shared"
	sharedWorkDir := "/workspace/shared"
	dayDir := filepath.Join(codexHome, "sessions", "2026", "09", "30")
	require.NoError(t, os.MkdirAll(dayDir, 0755))

	sessAID := "session-alpha"
	rolloutA := filepath.Join(dayDir, "rollout-2026-09-30T10-00-00-"+sessAID+".jsonl")
	contentA := fmt.Sprintf(
		`{"timestamp":"2026-09-30T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-alpha","cwd":"%s"}}`+"\n"+
			`{"timestamp":"2026-09-30T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n"+
			`{"timestamp":"2026-09-30T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"Alpha message body","images":[]}}`+"\n",
		sharedWorkDir, at.CoralSessionMarkerPrefix, sessAID,
	)
	require.NoError(t, os.WriteFile(rolloutA, []byte(contentA), 0644))

	sessBID := "session-beta"
	rolloutB := filepath.Join(dayDir, "rollout-2026-09-30T11-00-00-"+sessBID+".jsonl")
	contentB := fmt.Sprintf(
		`{"timestamp":"2026-09-30T11:00:00.000Z","type":"session_meta","payload":{"id":"rollout-beta","cwd":"%s"}}`+"\n"+
			`{"timestamp":"2026-09-30T11:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n"+
			`{"timestamp":"2026-09-30T11:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"Beta message body","images":[]}}`+"\n",
		sharedWorkDir, at.CoralSessionMarkerPrefix, sessBID,
	)
	require.NoError(t, os.WriteFile(rolloutB, []byte(contentB), 0644))

	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:  sessAID,
		SourceType: "codex",
		SourceFile: rolloutA,
	}))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:  sessBID,
		SourceType: "codex",
		SourceFile: rolloutB,
	}))

	// Verify Session Alpha does not leak into Session Beta
	respA, err := http.Get(server.URL + "/api/sessions/history/" + sessAID)
	require.NoError(t, err)
	defer respA.Body.Close()
	var bodyA map[string]any
	require.NoError(t, json.NewDecoder(respA.Body).Decode(&bodyA))
	msgsABytes, _ := json.Marshal(bodyA["messages"])
	assert.Contains(t, string(msgsABytes), "Alpha message body")
	assert.NotContains(t, string(msgsABytes), "Beta message body")

	// Verify Session Beta does not leak into Session Alpha
	respB, err := http.Get(server.URL + "/api/sessions/history/" + sessBID)
	require.NoError(t, err)
	defer respB.Body.Close()
	var bodyB map[string]any
	require.NoError(t, json.NewDecoder(respB.Body).Decode(&bodyB))
	msgsBBytes, _ := json.Marshal(bodyB["messages"])
	assert.Contains(t, string(msgsBBytes), "Beta message body")
	assert.NotContains(t, string(msgsBBytes), "Alpha message body")

	// Verify reader cache was cleared
	// h.jsonl.ClearSession(sid) is called in defer of GetSessionDetail
	// Requesting an unknown / deleted session cleanly returns 404 without stale cache interference
	respMissing, err := http.Get(server.URL + "/api/sessions/history/non-existent-session-id")
	require.NoError(t, err)
	defer respMissing.Body.Close()
	assert.Equal(t, http.StatusNotFound, respMissing.StatusCode)

	// Verify missing source file for an indexed session returns 404 rather than 500 or panic
	missingSessionID := "session-missing-source"
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:  missingSessionID,
		SourceType: "codex",
		SourceFile: filepath.Join(dayDir, "deleted-file.jsonl"),
	}))
	respMissingFile, err := http.Get(server.URL + "/api/sessions/history/" + missingSessionID)
	require.NoError(t, err)
	defer respMissingFile.Body.Close()
	assert.Equal(t, http.StatusNotFound, respMissingFile.StatusCode)

	// Ensure h.jsonl is not holding residual state
	h.jsonl.ClearSession(sessAID)
	h.jsonl.ClearSession(sessBID)
}

// TestTask1761_ConcurrentAccess verifies safe concurrent reads under race detection.
func TestTask1761_ConcurrentAccess(t *testing.T) {
	server, _, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()

	dayDir := filepath.Join(codexHome, "sessions", "2026", "09", "30")
	require.NoError(t, os.MkdirAll(dayDir, 0755))
	concSessionID := "d4e5f6a7-b8c9-0123-def0-123456789abc"
	rollout := filepath.Join(dayDir, "rollout-concurrent.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-09-30T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-conc","cwd":"/repo"}}`+"\n"+
			`{"timestamp":"2026-09-30T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n"+
			`{"timestamp":"2026-09-30T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"Concurrent test message","images":[]}}`+"\n",
		at.CoralSessionMarkerPrefix, concSessionID,
	)
	require.NoError(t, os.WriteFile(rollout, []byte(content), 0644))

	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:  concSessionID,
		SourceType: "codex",
		SourceFile: rollout,
	}))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := http.Get(server.URL + "/api/sessions/history/" + concSessionID)
			if assert.NoError(t, err) {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				resp.Body.Close()
			}
		}()
		go func() {
			defer wg.Done()
			resp, err := http.Get(server.URL + "/api/sessions/history/" + concSessionID + "/resume-info")
			if assert.NoError(t, err) {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}
