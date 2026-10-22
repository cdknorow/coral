package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// A person's message to an agent whose hook programs are gone (#6089): the app it was started from was replaced while it ran, so its UserPromptSubmit hook cannot run, and Coral gets no event for a
// message the agent took. Before, Chat said "may not have been sent" for a message that arrived. Now the message is confirmed by the agent's transcript, then by its echo on the screen; with
// neither, Coral says plainly that it can't reach the agent's hooks and how to fix that.

type hookHarness struct {
	term *agentTerm
	send func(string) *httptest.ResponseRecorder
	sid  string
	h    *SessionsHandler
}

func setupHooksGone(t *testing.T, hookDir string) hookHarness {
	t.Helper()
	_, handler, mock, ss := setupSessionsTestServer(t)
	sid := "eeeeeeee-6089-4000-8000-000000000001"
	name := naming.SessionName("codex", sid)
	mock.addSession(name, "/tmp/project")
	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "chat", WorkingDir: "/tmp/project"}))
	require.NoError(t, ss.SetHookDir(ctx, sid, hookDir))
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "stop", Summary: "stop"}) // it has started: the hooks worked once
	require.NoError(t, err)
	term := &agentTerm{mockSessionTerminal: mock}
	handler.terminal = term
	old := []time.Duration{humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait}
	humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait = 400*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond, 80*time.Millisecond
	prevReader := sendTranscriptReader
	t.Cleanup(func() {
		humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait = old[0], old[1], old[2], old[3]
		sendTranscriptReader = prevReader
	})
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/send", handler.Send)
	return hookHarness{term: term, sid: sid, h: handler, send: func(text string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"command": text, "agent_type": "codex", "session_id": sid})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/send", bytes.NewReader(b))
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}}
}

// a tools folder that is gone, and one that is there
func goneDir(t *testing.T) string {
	return filepath.Join(t.TempDir(), "Coral.app", "Contents", "MacOS")
}
func liveDir(t *testing.T) string {
	d := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(d, "coral-hook-agentic-state"), []byte("#!/bin/sh\n"), 0o755))
	return d
}

func TestHookDirGone(t *testing.T) {
	assert.False(t, hookDirGone(""), "no folder recorded: nothing is known")
	assert.True(t, hookDirGone(goneDir(t)), "the folder is gone")
	assert.True(t, hookDirGone(t.TempDir()), "the folder is there with no coral-hook program in it")
	assert.False(t, hookDirGone(liveDir(t)), "a hook program is there")
}

func TestHookDirIsRecordedAtEveryStartAndFlagsTheAgent(t *testing.T) {
	h := setupHooksGone(t, goneDir(t))
	assert.True(t, h.h.hooksUnreachable(context.Background(), h.sid))
	require.NoError(t, h.h.ss.SetHookDir(context.Background(), h.sid, liveDir(t)))
	assert.False(t, h.h.hooksUnreachable(context.Background(), h.sid))
	assert.False(t, h.h.hooksUnreachable(context.Background(), "ffffffff-0000-4000-8000-000000000000"), "an agent with no record is not flagged")
}

func TestHumanSendWithGoneHooks(t *testing.T) {
	type result struct {
		code string
		body map[string]any
	}
	run := func(h hookHarness, text string) (int, result) {
		rec := h.send(text)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		c, _ := out["code"].(string)
		return rec.Code, result{c, out}
	}

	t.Run("nothing confirms it: said plainly, not 'may not have been sent'", func(t *testing.T) {
		h := setupHooksGone(t, goneDir(t))
		h.term.show(codexReady(t, ""))
		h.term.onType = func(text string) { h.term.show(codexReady(t, "")) } // taken, the box is empty again, and no event comes
		code, res := run(h, "ship it")
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "hooks_unreachable", res.code)
		msg, _ := res.body["error"].(string)
		assert.Contains(t, msg, "can't reach this agent's hooks")
		assert.Contains(t, msg, "reopen Coral or restart the agent")
		assert.NotContains(t, msg, "may not have been sent")
		assert.Equal(t, false, res.body["not_sent"], "it may well have arrived: the composer does not take it back")
	})
	t.Run("the screen echo confirms it", func(t *testing.T) {
		h := setupHooksGone(t, goneDir(t))
		h.term.show(codexReady(t, ""))
		// The agent's echo of the message is in its history, above an input box that is empty again: not below the footer, where no screen draws it
		// and where the box reader (rightly) reads it as the box holding something.
		h.term.onType = func(text string) {
			h.term.show(strings.Replace(codexReady(t, ""), "  Tip:", "› "+text+"\n\n  Tip:", 1))
		}
		code, res := run(h, "deploy to staging now")
		assert.Equal(t, http.StatusOK, code, res.body)
	})
	t.Run("the transcript confirms it", func(t *testing.T) {
		h := setupHooksGone(t, goneDir(t))
		h.term.show(codexReady(t, ""))
		var mu sync.Mutex
		window := []map[string]any{{"type": "assistant", "content": "ready"}}
		sendTranscriptReader = func(*SessionsHandler, context.Context, string, string) *jsonl.Transcript {
			mu.Lock()
			defer mu.Unlock()
			return &jsonl.Transcript{Window: append([]map[string]any(nil), window...), Total: len(window)}
		}
		h.term.onType = func(text string) {
			h.term.show(codexReady(t, ""))
			mu.Lock()
			window = append(window, map[string]any{"type": "user", "content": []any{map[string]any{"type": "text", "text": text}}})
			mu.Unlock()
		}
		code, res := run(h, "please run the migration")
		assert.Equal(t, http.StatusOK, code, res.body)
	})
	t.Run("a transcript that gained some other message is no evidence", func(t *testing.T) {
		h := setupHooksGone(t, goneDir(t))
		h.term.show(codexReady(t, ""))
		var mu sync.Mutex
		window := []map[string]any{{"type": "assistant", "content": "ready"}}
		sendTranscriptReader = func(*SessionsHandler, context.Context, string, string) *jsonl.Transcript {
			mu.Lock()
			defer mu.Unlock()
			return &jsonl.Transcript{Window: append([]map[string]any(nil), window...), Total: len(window)}
		}
		h.term.onType = func(text string) {
			h.term.show(codexReady(t, ""))
			mu.Lock()
			window = append(window, map[string]any{"type": "user", "content": "a different message entirely"})
			mu.Unlock()
		}
		code, res := run(h, "please run the migration")
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "hooks_unreachable", res.code)
	})
	t.Run("hooks that are there: a message nothing confirms is still 'not confirmed'", func(t *testing.T) {
		h := setupHooksGone(t, liveDir(t))
		h.term.show(codexReady(t, ""))
		h.term.onType = func(text string) { h.term.show(codexReady(t, "")) }
		code, res := run(h, "ship it")
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "not_confirmed", res.code, "unchanged for an agent whose hooks can run")
	})
	t.Run("a screen read that outlasts the confirm wait does not make the pass give up before it looked at the transcript", func(t *testing.T) {
		h := setupHooksGone(t, liveDir(t))
		h.term.show(codexReady(t, ""))
		var mu sync.Mutex
		window := []map[string]any{{"type": "assistant", "content": "ready"}}
		sendTranscriptReader = func(*SessionsHandler, context.Context, string, string) *jsonl.Transcript {
			mu.Lock()
			defer mu.Unlock()
			return &jsonl.Transcript{Window: append([]map[string]any(nil), window...), Total: len(window)}
		}
		h.term.onType = func(text string) {
			h.term.show(codexReady(t, ""))
			mu.Lock()
			window = append(window, map[string]any{"type": "user", "content": text})
			mu.Unlock()
			h.term.screenDelay.Store(int64(200 * time.Millisecond)) // the confirm wait is 80 ms: every pass's screen read ends after it began
		}
		code, res := run(h, "please run the migration")
		assert.Equal(t, http.StatusOK, code, res.body)
	})
	t.Run("hooks that are there but sent nothing: the transcript still confirms it", func(t *testing.T) {
		h := setupHooksGone(t, liveDir(t))
		h.term.show(codexReady(t, ""))
		var mu sync.Mutex
		window := []map[string]any{{"type": "assistant", "content": "ready"}}
		sendTranscriptReader = func(*SessionsHandler, context.Context, string, string) *jsonl.Transcript {
			mu.Lock()
			defer mu.Unlock()
			return &jsonl.Transcript{Window: append([]map[string]any(nil), window...), Total: len(window)}
		}
		h.term.onType = func(text string) {
			h.term.show(codexReady(t, ""))
			mu.Lock()
			window = append(window, map[string]any{"type": "user", "content": strings.ToUpper(text[:0]) + text})
			mu.Unlock()
		}
		code, res := run(h, "please run the migration")
		assert.Equal(t, http.StatusOK, code, res.body)
	})
}

// A server whose tools folder holds coral-board and no hook programs (source "this build (coral-board only)") started its agents with hooks found elsewhere on their PATH: nothing is recorded, so
// no agent of it is ever flagged or told "can't reach this agent's hooks" (#6089 review).
func TestAToolsFolderWithNoHooksIsNotRecordedAndNeverFlagged(t *testing.T) {
	boardOnly := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(boardOnly, "coral-board"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("CORAL_HOOK_DIR", boardOnly)
	h := setupHooksGone(t, "")
	ctx := context.Background()
	h.h.noteLaunchedHooks(ctx, h.sid, "codex")
	dirs, err := h.h.ss.HookDirs(ctx)
	require.NoError(t, err)
	assert.NotContains(t, dirs, h.sid, "a folder with no hook program is not recorded")
	assert.False(t, h.h.hooksUnreachable(ctx, h.sid))
	// even when the folder later disappears, nothing was recorded: nothing is flagged
	require.NoError(t, os.RemoveAll(boardOnly))
	assert.False(t, h.h.hooksUnreachable(ctx, h.sid))

	// a folder with a hook is recorded, and is flagged only when it is lost
	withHooks := liveDir(t)
	t.Setenv("CORAL_HOOK_DIR", withHooks)
	h.h.noteLaunchedHooks(ctx, h.sid, "codex")
	dirs, err = h.h.ss.HookDirs(ctx)
	require.NoError(t, err)
	assert.Equal(t, withHooks, dirs[h.sid])
	assert.False(t, h.h.hooksUnreachable(ctx, h.sid))
	require.NoError(t, os.RemoveAll(withHooks))
	assert.True(t, h.h.hooksUnreachable(ctx, h.sid), "had hooks, lost them")
}
