package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

type testHistoryEnv struct {
	server     *httptest.Server
	handler    *SessionsHandler
	ss         *store.SessionStore
	codexHome  string
	brainHome  string
	claudeHome string
}

func setupAgentHistoryTestEnv(t *testing.T) *testHistoryEnv {
	t.Helper()

	dbDir := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(dbDir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	boardDBDir := t.TempDir()
	bs, err := board.NewStoreWithKey(filepath.Join(boardDBDir, "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	brainHome := filepath.Join(t.TempDir(), "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainHome)

	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", claudeHome)

	cfg := &config.Config{}
	h := &SessionsHandler{
		db:    db,
		ss:    store.NewSessionStore(db),
		ts:    store.NewTaskStore(db),
		bs:    bs,
		cfg:   cfg,
		jsonl: jsonl.NewSessionReader(),
	}

	r := chi.NewRouter()
	// Requests are tagged as the identity middleware tags them: the agent a request names with testAgentHeader by its
	// verified session, everything else as the dashboard (the Operator), which may name any session (#4512).
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if sid := req.Header.Get(testAgentHeader); sid != "" {
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "agent-" + sid}))
			} else {
				req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/agent/history/search", h.SearchAgentHistory)
	r.Get("/api/agent/history/context", h.GetAgentHistoryContext)
	r.Get("/api/sessions/live/{name}/history/search", h.SearchAgentHistoryForLive)
	r.Get("/api/sessions/live/{name}/history/context", h.GetAgentHistoryContextForLive)

	server := httptest.NewServer(r)
	t.Cleanup(func() { server.Close() })

	return &testHistoryEnv{
		server:     server,
		handler:    h,
		ss:         h.ss,
		codexHome:  codexHome,
		brainHome:  brainHome,
		claudeHome: claudeHome,
	}
}

// TestAgentHistorySearch_FactsAndSemantics tests:
// 1. Recovering prior assistant facts and user instructions.
// 2. Exact quoted phrase vs unquoted keyword search.
// 3. Case insensitivity and bounded excerpts.
func TestAgentHistorySearch_FactsAndSemantics(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	sessID := "00000000-0000-4000-8000-000000000001"
	workDir := filepath.Join(env.claudeHome, "my-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))

	// Register live session in DB
	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sessID,
		AgentType:  at.Claude,
		AgentName:  "backend-worker",
		WorkingDir: workDir,
	}))

	// Claude project transcript: ~/.claude/projects/-path-encoded/sessionID.jsonl
	projDir := filepath.Join(env.claudeHome, strings.ReplaceAll(workDir, "/", "-"))
	require.NoError(t, os.MkdirAll(projDir, 0755))
	transcriptPath := filepath.Join(projDir, sessID+".jsonl")

	transcriptData := `{"type":"user","timestamp":"2026-09-30T10:00:00Z","message":{"content":"Please configure the server port to 8420 and disable telemetry."}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:00:05Z","message":{"content":"I have configured the server to listen on port 8420 and disabled telemetry in config.json."}}` + "\n" +
		`{"type":"user","timestamp":"2026-09-30T10:01:00Z","message":{"content":"What database engine are we using for storing task items?"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:01:10Z","message":{"content":"We are using SQLite FTS5 for all internal task and message indexing."}}` + "\n"

	require.NoError(t, os.WriteFile(transcriptPath, []byte(transcriptData), 0644))

	// 1. Search for assistant statement: "port 8420"
	resp, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessID + "&query=" + "port+8420")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var searchResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&searchResp))
	assert.Equal(t, "complete", searchResp.Status)
	assert.Equal(t, 2, searchResp.TotalMatches, "both user and assistant mentioned port 8420")
	assert.True(t, strings.Contains(searchResp.Results[0].Excerpt, "port 8420"))

	// 2. Filter by role: roles=assistant
	respAssis, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessID + "&query=port+8420&roles=assistant")
	require.NoError(t, err)
	defer respAssis.Body.Close()
	var searchAssis AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respAssis.Body).Decode(&searchAssis))
	assert.Equal(t, 1, searchAssis.TotalMatches)
	assert.Equal(t, "assistant", searchAssis.Results[0].Role)
	assert.Contains(t, searchAssis.Results[0].Excerpt, "configured the server to listen on port 8420")

	// 3. Quoted phrase search: "database engine" vs words that are separated
	respPhrase, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessID + "&query=%22database+engine%22")
	require.NoError(t, err)
	defer respPhrase.Body.Close()
	var searchPhrase AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respPhrase.Body).Decode(&searchPhrase))
	assert.Equal(t, 1, searchPhrase.TotalMatches)
	assert.Equal(t, "user", searchPhrase.Results[0].Role)

	// 4. Case-insensitivity: "SQLITE FTS5" matches "SQLite FTS5"
	respCase, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessID + "&query=SQLITE+FTS5")
	require.NoError(t, err)
	defer respCase.Body.Close()
	var searchCase AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respCase.Body).Decode(&searchCase))
	assert.Equal(t, 1, searchCase.TotalMatches)
	assert.Contains(t, searchCase.Results[0].Excerpt, "SQLite FTS5")
}

// TestAgentHistorySearch_ResumedLineage verifies:
// 1. An agent session correctly discovers facts in its ancestor session (via resume_from_id).
// 2. Multi-tier lineage: S2 -> S1 -> S0.
// 3. Setting include_ancestors=false isolates search to only the active session.
func TestAgentHistorySearch_ResumedLineage(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	workDir := filepath.Join(env.claudeHome, "lineage-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))
	projDir := filepath.Join(env.claudeHome, strings.ReplaceAll(workDir, "/", "-"))
	require.NoError(t, os.MkdirAll(projDir, 0755))

	s0ID := "00000000-0000-4000-8000-0000000000a0"
	s1ID := "00000000-0000-4000-8000-0000000000a1"
	s2ID := "00000000-0000-4000-8000-0000000000a2" // live session

	// S0 transcript (ancestor)
	s0File := filepath.Join(projDir, s0ID+".jsonl")
	s0Content := `{"type":"user","timestamp":"2026-09-30T08:00:00Z","message":{"content":"Project initial secret token is ALPHA-SECRET-99."}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T08:00:05Z","message":{"content":"Acknowledged secret ALPHA-SECRET-99."}}` + "\n"
	require.NoError(t, os.WriteFile(s0File, []byte(s0Content), 0644))

	// Index S0 in session_index
	firstTS := "2026-09-30T08:00:00Z"
	lastTS := "2026-09-30T08:00:05Z"
	require.NoError(t, env.ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      s0ID,
		SourceType:     "claude",
		SourceFile:     s0File,
		FirstTimestamp: &firstTS,
		LastTimestamp:  &lastTS,
		MessageCount:   2,
	}))

	// S1 transcript (intermediate ancestor, stopped live session)
	s1File := filepath.Join(projDir, s1ID+".jsonl")
	s1Content := `{"type":"user","timestamp":"2026-09-30T09:00:00Z","message":{"content":"Deploying staging cluster to US-East."}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T09:00:10Z","message":{"content":"Staging cluster deployed in US-East region."}}` + "\n"
	require.NoError(t, os.WriteFile(s1File, []byte(s1Content), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:    s1ID,
		AgentType:    at.Claude,
		AgentName:    "worker-1",
		WorkingDir:   workDir,
		ResumeFromID: ptr.NonEmpty(s0ID),
	}))

	// S2 transcript (current active session)
	s2File := filepath.Join(projDir, s2ID+".jsonl")
	s2Content := `{"type":"user","timestamp":"2026-09-30T10:00:00Z","message":{"content":"Now we are ready for production release."}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:00:15Z","message":{"content":"Production release initiated."}}` + "\n"
	require.NoError(t, os.WriteFile(s2File, []byte(s2Content), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:    s2ID,
		AgentType:    at.Claude,
		AgentName:    "worker-2",
		WorkingDir:   workDir,
		ResumeFromID: ptr.NonEmpty(s1ID),
	}))

	// Search from S2 for secret in S0: "ALPHA-SECRET-99"
	resp, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + s2ID + "&query=ALPHA-SECRET-99")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var searchResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&searchResp))
	assert.Equal(t, 2, searchResp.TotalMatches)
	assert.False(t, searchResp.Results[0].IsCurrent, "ancestor match is not current")
	assert.Equal(t, s0ID, searchResp.Results[0].SessionID)

	// Search for staging fact in S1: "US-East"
	respS1, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + s2ID + "&query=US-East")
	require.NoError(t, err)
	defer respS1.Body.Close()
	var searchRespS1 AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respS1.Body).Decode(&searchRespS1))
	assert.Equal(t, 2, searchRespS1.TotalMatches)
	assert.Equal(t, s1ID, searchRespS1.Results[0].SessionID)

	// Search with include_ancestors=false: ALPHA-SECRET-99 must NOT be found in S2
	respNoAnc, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + s2ID + "&query=ALPHA-SECRET-99&include_ancestors=false")
	require.NoError(t, err)
	defer respNoAnc.Body.Close()
	var searchRespNoAnc AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respNoAnc.Body).Decode(&searchRespNoAnc))
	assert.Equal(t, 0, searchRespNoAnc.TotalMatches)
}

// TestAgentHistorySearch_AuthorizationTrustBoundary verifies:
// 1. Agent cannot search another agent's session outside its lineage (403 Forbidden).
// 2. Unknown caller session returns 404.
// 3. Validation: empty query (400), query too long (400).
func TestAgentHistorySearch_AuthorizationTrustBoundary(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	workDir := filepath.Join(env.claudeHome, "auth-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))

	sessAlice := "00000000-0000-4000-8000-0000000000b1"
	sessBob := "00000000-0000-4000-8000-0000000000b2"

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sessAlice,
		AgentType:  at.Claude,
		AgentName:  "alice",
		WorkingDir: workDir,
	}))
	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sessBob,
		AgentType:  at.Claude,
		AgentName:  "bob",
		WorkingDir: workDir,
	}))

	// 1. Bob attempts to search Alice's history
	resp, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessBob + "&target_session_id=" + sessAlice + "&query=secret")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "searching session outside lineage must return 403 Forbidden")

	// 2. Unknown session caller
	respUnk, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=fake-unknown-session&query=hello")
	require.NoError(t, err)
	defer respUnk.Body.Close()
	assert.Equal(t, http.StatusNotFound, respUnk.StatusCode)

	// 3. Empty query
	respEmpty, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessAlice + "&query=")
	require.NoError(t, err)
	defer respEmpty.Body.Close()
	assert.Equal(t, http.StatusBadRequest, respEmpty.StatusCode)

	// 4. Query too long (> 1000 chars)
	longQuery := strings.Repeat("a", 1001)
	respLong, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessAlice + "&query=" + longQuery)
	require.NoError(t, err)
	defer respLong.Body.Close()
	assert.Equal(t, http.StatusBadRequest, respLong.StatusCode)

	// 5. Query with SQL injection attempt: must be safely processed
	respInj, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + sessAlice + "&query=%27+OR+1%3D1+--")
	require.NoError(t, err)
	defer respInj.Body.Close()
	assert.Equal(t, http.StatusOK, respInj.StatusCode)
}

// TestAgentHistorySearch_MultiProviderSupport verifies Codex and Antigravity transcripts.
func TestAgentHistorySearch_MultiProviderSupport(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	// 1. Antigravity session
	agySessID := "b2c3d4e5-f6a7-8901-bcde-f0123456789a"
	agyNativeConv := "c1d2e3f4-a5b6-7890-1234-567890abcdef"
	agyLogsDir := filepath.Join(env.brainHome, agyNativeConv, ".system_generated", "logs")
	require.NoError(t, os.MkdirAll(agyLogsDir, 0755))
	agyFile := filepath.Join(agyLogsDir, "transcript.jsonl")

	agyContent := fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"Coral session metadata:\n%s %s\nPlease check telemetry configuration."}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Telemetry is currently set to silent mode."}`+"\n",
		at.CoralSessionMarkerPrefix, agySessID)
	require.NoError(t, os.WriteFile(agyFile, []byte(agyContent), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  agySessID,
		AgentType:  at.Agy,
		AgentName:  "agy-agent",
		WorkingDir: env.brainHome,
	}))

	respAgy, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + agySessID + "&query=silent+mode")
	require.NoError(t, err)
	defer respAgy.Body.Close()
	assert.Equal(t, http.StatusOK, respAgy.StatusCode)
	var agyResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respAgy.Body).Decode(&agyResp))
	require.Equal(t, 1, agyResp.TotalMatches)
	assert.Equal(t, "assistant", agyResp.Results[0].Role)
	assert.Contains(t, agyResp.Results[0].Excerpt, "silent mode")

	// 2. Codex session
	codexSessID := "a1b2c3d4-e5f6-7890-abcd-ef0123456789"
	codexDayDir := filepath.Join(env.codexHome, "sessions", "2026", "09", "30")
	require.NoError(t, os.MkdirAll(codexDayDir, 0755))
	codexRolloutName := "rollout-2026-09-30T12-00-00-native-codex-123"
	codexFile := filepath.Join(codexDayDir, codexRolloutName+".jsonl")

	codexContent := fmt.Sprintf(`{"timestamp":"2026-09-30T12:00:00.000Z","type":"session_meta","payload":{"id":"%s","cwd":"/repo"}}`+"\n"+
		`{"timestamp":"2026-09-30T12:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n"+
		`{"timestamp":"2026-09-30T12:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"Check git branch status"}}`+"\n"+
		`{"timestamp":"2026-09-30T12:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"Git branch is cleanly up to date with origin/main."}}`+"\n",
		codexRolloutName, at.CoralSessionMarkerPrefix, codexSessID)
	require.NoError(t, os.WriteFile(codexFile, []byte(codexContent), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  codexSessID,
		AgentType:  at.Codex,
		AgentName:  "codex-agent",
		WorkingDir: "/repo",
	}))

	respCodex, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + codexSessID + "&query=origin/main")
	require.NoError(t, err)
	defer respCodex.Body.Close()
	assert.Equal(t, http.StatusOK, respCodex.StatusCode)
	var codexResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respCodex.Body).Decode(&codexResp))
	assert.Equal(t, 1, codexResp.TotalMatches)
	assert.Equal(t, "assistant", codexResp.Results[0].Role)
	assert.Contains(t, codexResp.Results[0].Excerpt, "origin/main")
}

// TestAgentHistorySearch_LiveAppendedAndUnavailableSources verifies:
// 1. Live appended content is immediately discoverable.
// 2. Missing/deleted transcript in lineage returns honest partial/unavailable status.
func TestAgentHistorySearch_LiveAppendedAndUnavailableSources(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	workDir := filepath.Join(env.claudeHome, "live-append-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))
	projDir := filepath.Join(env.claudeHome, strings.ReplaceAll(workDir, "/", "-"))
	require.NoError(t, os.MkdirAll(projDir, 0755))

	liveSessID := "00000000-0000-4000-8000-0000000000c1"
	ancestorSessID := "00000000-0000-4000-8000-0000000000c2"

	// Live transcript
	liveFile := filepath.Join(projDir, liveSessID+".jsonl")
	initialContent := `{"type":"user","timestamp":"2026-09-30T10:00:00Z","message":{"content":"Initial message"}}` + "\n"
	require.NoError(t, os.WriteFile(liveFile, []byte(initialContent), 0644))

	// Register live session referencing missing ancestor
	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:    liveSessID,
		AgentType:    at.Claude,
		AgentName:    "live-appender",
		WorkingDir:   workDir,
		ResumeFromID: ptr.NonEmpty(ancestorSessID),
	}))

	// Register missing ancestor in session_index with non-existent source_file
	missingFile := filepath.Join(projDir, "non_existent_file.jsonl")
	firstTS := "2026-09-30T09:00:00Z"
	lastTS := "2026-09-30T09:05:00Z"
	require.NoError(t, env.ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      ancestorSessID,
		SourceType:     "claude",
		SourceFile:     missingFile,
		FirstTimestamp: &firstTS,
		LastTimestamp:  &lastTS,
	}))

	// 1. Search initially - ancestor is unavailable, live has no match
	respInit, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + liveSessID + "&query=FRESH-APPENDED-KEYWORD")
	require.NoError(t, err)
	defer respInit.Body.Close()
	var initResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respInit.Body).Decode(&initResp))
	assert.Equal(t, "partial", initResp.Status, "status must be partial because ancestor is unavailable")
	assert.Equal(t, 0, initResp.TotalMatches)

	// 2. Append new turn to live transcript while running
	f, err := os.OpenFile(liveFile, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"assistant","timestamp":"2026-09-30T10:05:00Z","message":{"content":"Here is the FRESH-APPENDED-KEYWORD for verification."}}` + "\n")
	require.NoError(t, err)
	f.Close()

	// 3. Search again: FRESH-APPENDED-KEYWORD should be found immediately
	respAppended, err := http.Get(env.server.URL + "/api/agent/history/search?session_id=" + liveSessID + "&query=FRESH-APPENDED-KEYWORD")
	require.NoError(t, err)
	defer respAppended.Body.Close()
	var appendedResp AgentHistorySearchResponse
	require.NoError(t, json.NewDecoder(respAppended.Body).Decode(&appendedResp))
	assert.Equal(t, 1, appendedResp.TotalMatches)
	assert.Contains(t, appendedResp.Results[0].Excerpt, "FRESH-APPENDED-KEYWORD")
	assert.True(t, appendedResp.Results[0].IsCurrent)
}

// TestAgentHistoryContextEndpoint verifies /api/agent/history/context.
func TestAgentHistoryContextEndpoint(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	workDir := filepath.Join(env.claudeHome, "context-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))
	projDir := filepath.Join(env.claudeHome, strings.ReplaceAll(workDir, "/", "-"))
	require.NoError(t, os.MkdirAll(projDir, 0755))

	sessID := "00000000-0000-4000-8000-0000000000d1"
	sessFile := filepath.Join(projDir, sessID+".jsonl")

	transcriptData := `{"type":"user","timestamp":"2026-09-30T10:00:00Z","message":{"content":"Turn 0 user"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:01:00Z","message":{"content":"Turn 1 assistant"}}` + "\n" +
		`{"type":"user","timestamp":"2026-09-30T10:02:00Z","message":{"content":"Turn 2 user target"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:03:00Z","message":{"content":"Turn 3 assistant"}}` + "\n" +
		`{"type":"user","timestamp":"2026-09-30T10:04:00Z","message":{"content":"Turn 4 user"}}` + "\n"
	require.NoError(t, os.WriteFile(sessFile, []byte(transcriptData), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sessID,
		AgentType:  at.Claude,
		AgentName:  "context-agent",
		WorkingDir: workDir,
	}))

	// 1. Fetch context around turn 2 with window 1 (should return turns 1, 2, 3)
	resp, err := http.Get(fmt.Sprintf("%s/api/agent/history/context?session_id=%s&message_index=2&window=1", env.server.URL, sessID))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var ctxResp AgentHistoryContextResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ctxResp))
	assert.Equal(t, 2, ctxResp.TargetIndex)
	assert.Equal(t, 5, ctxResp.TotalMessages)
	assert.Equal(t, 3, len(ctxResp.Messages))

	assert.Equal(t, 1, ctxResp.Messages[0].MessageIndex)
	assert.False(t, ctxResp.Messages[0].IsTarget)

	assert.Equal(t, 2, ctxResp.Messages[1].MessageIndex)
	assert.True(t, ctxResp.Messages[1].IsTarget)
	assert.Contains(t, ctxResp.Messages[1].Content, "Turn 2 user target")

	assert.Equal(t, 3, ctxResp.Messages[2].MessageIndex)
	assert.False(t, ctxResp.Messages[2].IsTarget)

	// 2. Out of bounds index
	respOOB, err := http.Get(fmt.Sprintf("%s/api/agent/history/context?session_id=%s&message_index=99", env.server.URL, sessID))
	require.NoError(t, err)
	defer respOOB.Body.Close()
	assert.Equal(t, http.StatusNotFound, respOOB.StatusCode)
}

// TestAgentHistorySearch_ConcurrentRequests verifies safe concurrent access under -race.
func TestAgentHistorySearch_ConcurrentRequests(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()

	workDir := filepath.Join(env.claudeHome, "conc-project")
	require.NoError(t, os.MkdirAll(workDir, 0755))
	projDir := filepath.Join(env.claudeHome, strings.ReplaceAll(workDir, "/", "-"))
	require.NoError(t, os.MkdirAll(projDir, 0755))

	sessID := "00000000-0000-4000-8000-0000000000e1"
	sessFile := filepath.Join(projDir, sessID+".jsonl")

	transcriptData := `{"type":"user","timestamp":"2026-09-30T10:00:00Z","message":{"content":"Running concurrent stress check."}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-09-30T10:00:05Z","message":{"content":"Concurrency test passed successfully."}}` + "\n"
	require.NoError(t, os.WriteFile(sessFile, []byte(transcriptData), 0644))

	require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  sessID,
		AgentType:  at.Claude,
		AgentName:  "conc-worker",
		WorkingDir: workDir,
	}))

	var wg sync.WaitGroup
	numRoutines := 30
	wg.Add(numRoutines)

	for i := 0; i < numRoutines; i++ {
		go func(iter int) {
			defer wg.Done()
			q := "concurrent"
			if iter%2 == 0 {
				q = "passed"
			}
			resp, err := http.Get(fmt.Sprintf("%s/api/agent/history/search?session_id=%s&query=%s", env.server.URL, sessID, q))
			if err != nil {
				t.Errorf("request error: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("unexpected status: %d", resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
}

// An agent's token decides whose history it searches or reads (#4512, Security's #4508 H1): agent A's token with
// agent B's session_id is refused on every route, an agent that names no session gets its own, and the Operator
// still names any.
func TestAgentHistoryIsBoundToTheCallersToken(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()
	workDir := filepath.Join(env.claudeHome, "bound-project")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	for _, s := range []struct{ id, name string }{{"sess-a", "alice"}, {"sess-b", "bob"}} {
		require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: s.id, AgentType: at.Claude, AgentName: s.name, WorkingDir: workDir}))
	}
	get := func(agent, path string) (int, string) {
		req, _ := http.NewRequest("GET", env.server.URL+path, nil)
		if agent != "" {
			req.Header.Set(testAgentHeader, agent)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, route := range []string{"/api/agent/history/search?query=secret&", "/api/agent/history/context?message_index=0&", "/api/sessions/live/bob/history/search?query=secret&", "/api/sessions/live/bob/history/context?message_index=0&"} {
		// Alice's token, Bob's session: refused (403 identity_mismatch), and nothing of Bob's is searched.
		code, body := get("sess-a", route+"session_id=sess-b")
		assert.Equal(t, http.StatusForbidden, code, route)
		assert.Contains(t, body, "identity_mismatch", route)
		// Her own session named: allowed past the binding (the lineage rules then decide).
		code, _ = get("sess-a", route+"session_id=sess-a")
		assert.NotEqual(t, http.StatusForbidden, code, route+" own session")
		assert.NotEqual(t, http.StatusUnauthorized, code, route+" own session")
		// The Operator (the dashboard) names any session.
		code, _ = get("", route+"session_id=sess-b")
		assert.NotEqual(t, http.StatusForbidden, code, route+" Operator")
		assert.NotEqual(t, http.StatusUnauthorized, code, route+" Operator")
	}
	// The live route names Bob by his agent name: Alice's token is refused there too, though she named no session_id.
	code, body := get("sess-a", "/api/sessions/live/bob/history/search?query=secret")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "identity_mismatch")
	// An agent that names no session searches its own: the token decides.
	code, _ = get("sess-a", "/api/agent/history/search?query=hello")
	assert.Equal(t, http.StatusOK, code)
	// Nothing at all (no token, not the dashboard) is refused, not given a session.
	req, _ := http.NewRequest("GET", env.server.URL+"/api/agent/history/search?query=hello", nil)
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Get("/api/agent/history/search", env.handler.SearchAgentHistory)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
