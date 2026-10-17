package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4447: Settings > Storage deletes the terminal logs of ended agents, after a confirmation, by exact name inside the logs folder, never those of a live,
// sleeping or archived agent, and a log the sessions database cannot vouch for is kept.

type logsRig struct {
	t      *testing.T
	srv    *httptest.Server
	h      *SessionsHandler
	ss     *store.SessionStore
	logDir string
}

func newLogsRig(t *testing.T) *logsRig {
	t.Helper()
	cfg := config.Load(t.TempDir())
	cfg.LogDir = t.TempDir() // never the real logs folder
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	r := chi.NewRouter()
	ident := testIdentity(h.db)
	r.Use(func(next http.Handler) http.Handler {
		withIdent := ident(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(testDashboardHeader) != "" {
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
				return
			}
			withIdent.ServeHTTP(w, req)
		})
	})
	h.RegisterTerminalLogs(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &logsRig{t: t, srv: srv, h: h, ss: ss, logDir: cfg.LogDir}
}

func (g *logsRig) call(method, path string, header map[string]string, body any) (int, map[string]any) {
	g.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, g.srv.URL+path, rd)
	require.NoError(g.t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(g.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// session registers a session row with the given state; status "inactive" is an ended one (UnregisterLiveSession).
func (g *logsRig) session(id, agentType string, ended, sleeping, archived bool) {
	g.t.Helper()
	ctx := context.Background()
	require.NoError(g.t, g.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: "a-" + id[:4], AgentType: agentType, WorkingDir: g.t.TempDir()}))
	if ended {
		require.NoError(g.t, g.ss.UnregisterLiveSession(ctx, id))
	}
	_, err := g.h.db.ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = ?, is_archived = ? WHERE session_id = ?", b2i(sleeping), b2i(archived), id)
	require.NoError(g.t, err)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (g *logsRig) log(agentType, id string, size int) string {
	g.t.Helper()
	name := agentType + "_coral_" + id + ".log"
	require.NoError(g.t, os.WriteFile(filepath.Join(g.logDir, name), bytes.Repeat([]byte("x"), size), 0o600))
	return name
}

func (g *logsRig) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(g.logDir, name))
	return err == nil
}

const (
	idEnded1   = "11111111-1111-4111-8111-111111111111"
	idEnded2   = "22222222-2222-4222-8222-222222222222"
	idLive     = "33333333-3333-4333-8333-333333333333"
	idSleeping = "44444444-4444-4444-8444-444444444444"
	idArchived = "55555555-5555-4555-8555-555555555555"
	idEndedZZ  = "66666666-6666-4666-8666-666666666666" // ended, but a codex log named as claude's
	idNoRow    = "77777777-7777-4777-8777-777777777777"
	idEndedSlp = "88888888-8888-4888-8888-888888888888" // stopped, but asleep: wake needs the log
)

func TestDeleteEndedTerminalLogsOnlyEndedAgents(t *testing.T) {
	g := newLogsRig(t)
	g.session(idEnded1, "claude", true, false, false)
	g.session(idEnded2, "codex", true, false, false)
	g.session(idLive, "claude", false, false, false)
	g.session(idSleeping, "claude", false, true, false)
	g.session(idArchived, "claude", true, false, true)
	g.session(idEndedZZ, "codex", true, false, false)
	g.session(idEndedSlp, "claude", true, true, false)
	gone := map[string]int{g.log("claude", idEnded1, 100): 100, g.log("codex", idEnded2, 250): 250}
	kept := []string{
		g.log("claude", idLive, 10), g.log("claude", idSleeping, 20), g.log("claude", idArchived, 30),
		g.log("claude", idEndedZZ, 40), // the row says codex: not vouched for
		g.log("claude", idNoRow, 50),   // no row: not vouched for
		g.log("claude", idEndedSlp, 60),
	}
	// Things that are not Coral's terminal logs stay, whatever they are called.
	require.NoError(t, os.WriteFile(filepath.Join(g.logDir, "notes.txt"), []byte("mine"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(g.logDir, "claude_coral_"+idEnded1+".log.1"), []byte("rotated"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(g.logDir, "claude_coral_"+idEnded2+"dir.log"), 0o700))
	outside := filepath.Join(t.TempDir(), "outside.log")
	require.NoError(t, os.WriteFile(outside, []byte("outside the folder"), 0o600))
	if err := os.Symlink(outside, filepath.Join(g.logDir, "claude_coral_"+idEnded2+"link.log")); err == nil {
		kept = append(kept, "claude_coral_"+idEnded2+"link.log")
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	dash := map[string]string{testDashboardHeader: "1"}

	code, out := g.call("GET", "/api/system/terminal-logs", dash, nil)
	require.Equal(t, http.StatusOK, code, out)
	ended := out["ended"].(map[string]any)
	assert.EqualValues(t, 2, ended["count"], "only the two ended agents' logs: %v", out)
	assert.EqualValues(t, 350, ended["bytes"])
	assert.EqualValues(t, 10+20+30+40+50+60+100+250, out["bytes"], "every Coral-named log is counted in the total, nothing else")

	// Operator only, the status as well: it names the size of other agents' logs.
	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"}} {
		code, out := g.call("GET", "/api/system/terminal-logs", hdr, nil)
		assert.Equal(t, http.StatusForbidden, code, "status: "+name)
		assert.Equal(t, "operator_only", out["code"], "status: "+name)
	}
	// Operator only.
	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"}} {
		code, out := g.call("POST", "/api/system/terminal-logs/delete-ended", hdr, map[string]any{"count": 2, "bytes": 350})
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "operator_only", out["code"], name)
	}
	for name := range gone {
		assert.True(t, g.exists(name), "still there after the refusals: %s", name)
	}
	for name, body := range map[string]any{"no body": nil, "no count": map[string]any{"bytes": 350}, "no size": map[string]any{"count": 2}} {
		code, _ := g.call("POST", "/api/system/terminal-logs/delete-ended", dash, body)
		assert.Equal(t, http.StatusBadRequest, code, name)
	}

	// What was shown must be what is deleted: a different count or size deletes nothing.
	for name, body := range map[string]map[string]any{"another count": {"count": 3, "bytes": 350}, "another size": {"count": 2, "bytes": 351}} {
		code, out := g.call("POST", "/api/system/terminal-logs/delete-ended", dash, body)
		assert.Equal(t, http.StatusConflict, code, name)
		assert.Equal(t, "changed", out["code"], name)
	}
	for name := range gone {
		assert.True(t, g.exists(name), "nothing deleted on a mismatch: %s", name)
	}

	code, out = g.call("POST", "/api/system/terminal-logs/delete-ended", dash, map[string]any{"count": 2, "bytes": 350})
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 2, out["deleted"])
	assert.EqualValues(t, 350, out["bytes"], "what is freed is what was shown")
	assert.Empty(t, out["failed"])
	for name := range gone {
		assert.False(t, g.exists(name), "deleted: %s", name)
	}
	for _, name := range kept {
		assert.True(t, g.exists(name), "kept: %s", name)
	}
	for _, name := range []string{"notes.txt", "claude_coral_" + idEnded1 + ".log.1", "claude_coral_" + idEnded2 + "dir.log"} {
		assert.True(t, g.exists(name), "not a terminal log, kept: %s", name)
	}
	_, err := os.Stat(outside)
	assert.NoError(t, err, "nothing outside the logs folder was touched")

	// Asking again: nothing is left to delete, and the old numbers are a mismatch.
	code, out = g.call("POST", "/api/system/terminal-logs/delete-ended", dash, map[string]any{"count": 2, "bytes": 350})
	assert.Equal(t, http.StatusConflict, code, out)
	code, out = g.call("POST", "/api/system/terminal-logs/delete-ended", dash, map[string]any{"count": 0, "bytes": 0})
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 0, out["deleted"])
}

func TestTerminalLogsFailClosed(t *testing.T) {
	g := newLogsRig(t)
	g.session(idEnded1, "claude", true, false, false)
	name := g.log("claude", idEnded1, 100)
	dash := map[string]string{testDashboardHeader: "1"}

	// The sessions database cannot be read: no answer a deletion could rest on, and nothing is deleted.
	ss := g.h.ss
	g.h.ss = nil
	code, out := g.call("GET", "/api/system/terminal-logs", dash, nil)
	assert.Equal(t, http.StatusServiceUnavailable, code, out)
	code, out = g.call("POST", "/api/system/terminal-logs/delete-ended", dash, map[string]any{"count": 1, "bytes": 100})
	assert.Equal(t, http.StatusServiceUnavailable, code, out)
	assert.True(t, g.exists(name))
	g.h.ss = ss

	// A logs folder that is not there: an empty answer, not an error, and nothing to delete.
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	old := g.h.cfg.LogDir
	g.h.cfg.LogDir = missing
	code, out = g.call("GET", "/api/system/terminal-logs", dash, nil)
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 0, out["files"])
	g.h.cfg.LogDir = old
	assert.True(t, g.exists(name))
}

// A log whose agent is woken, or archived, between the look and the removal is kept: the state is asked again.
func TestStillEndedAsksAgain(t *testing.T) {
	name := "claude_coral_" + idEnded1 + ".log"
	ended := map[string]store.LiveSessionState{idEnded1: {AgentType: "claude", Status: "inactive"}}
	assert.True(t, stillEnded(ended, name))
	for what, st := range map[string]store.LiveSessionState{
		"awake again":      {AgentType: "claude", Status: "active"},
		"asleep":           {AgentType: "claude", Status: "inactive", Sleeping: true},
		"archived":         {AgentType: "claude", Status: "inactive", Archived: true},
		"another type":     {AgentType: "codex", Status: "inactive"},
		"an empty status":  {AgentType: "claude"},
		"a status unknown": {AgentType: "claude", Status: "stopping"},
	} {
		assert.False(t, stillEnded(map[string]store.LiveSessionState{idEnded1: st}, name), what)
	}
	assert.False(t, stillEnded(map[string]store.LiveSessionState{}, name), "no row")
	assert.False(t, stillEnded(ended, "notes.txt"), "not a log name")
	assert.False(t, stillEnded(ended, strings.ReplaceAll(name, ".log", ".log.1")), "a rotated copy is not a log name")
}

// An agent woken between the look and the removal keeps its log: the session is asked again just before each removal.
func TestEndedLogOfAnAgentWokenMeanwhileIsKept(t *testing.T) {
	g := newLogsRig(t)
	g.session(idEnded1, "claude", true, false, false)
	g.session(idEnded2, "codex", true, false, false)
	woken := g.log("claude", idEnded1, 100)
	other := g.log("codex", idEnded2, 250)
	terminalLogsBeforeRecheck = func(string) {
		_, err := g.h.db.ExecContext(context.Background(), "UPDATE live_sessions SET status = 'active' WHERE session_id = ?", idEnded1)
		require.NoError(t, err)
	}
	t.Cleanup(func() { terminalLogsBeforeRecheck = nil })
	code, out := g.call("POST", "/api/system/terminal-logs/delete-ended", map[string]string{testDashboardHeader: "1"}, map[string]any{"count": 2, "bytes": 350})
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 1, out["deleted"])
	assert.EqualValues(t, 250, out["bytes"])
	assert.Len(t, out["failed"], 1, "the woken agent's log is reported, not deleted: %v", out)
	assert.True(t, g.exists(woken), "its log is kept")
	assert.False(t, g.exists(other))
}

// A session that falls asleep or is archived between the look and the removal keeps its log: the recheck asks for both, not only for the status.
func TestEndedLogOfAnAgentAsleepOrArchivedMeanwhileIsKept(t *testing.T) {
	for what, set := range map[string]string{"asleep": "is_sleeping = 1", "archived": "is_archived = 1"} {
		t.Run(what, func(t *testing.T) {
			g := newLogsRig(t)
			g.session(idEnded1, "claude", true, false, false)
			g.session(idEnded2, "codex", true, false, false)
			changed := g.log("claude", idEnded1, 100)
			other := g.log("codex", idEnded2, 250)
			terminalLogsBeforeRecheck = func(string) {
				_, err := g.h.db.ExecContext(context.Background(), "UPDATE live_sessions SET "+set+" WHERE session_id = ?", idEnded1)
				require.NoError(t, err)
			}
			t.Cleanup(func() { terminalLogsBeforeRecheck = nil })
			code, out := g.call("POST", "/api/system/terminal-logs/delete-ended", map[string]string{testDashboardHeader: "1"}, map[string]any{"count": 2, "bytes": 350})
			require.Equal(t, http.StatusOK, code, out)
			assert.EqualValues(t, 1, out["deleted"])
			assert.True(t, g.exists(changed), "its log is kept")
			assert.False(t, g.exists(other))
		})
	}
}

// Each log is checked just before its own removal, not once for the batch: an agent woken while the first logs are being removed keeps its log.
func TestEndedLogWokenAfterTheFirstRemovalIsKept(t *testing.T) {
	g := newLogsRig(t)
	g.session(idEnded1, "claude", true, false, false)
	g.session(idEnded2, "codex", true, false, false)
	first := g.log("claude", idEnded1, 100)
	second := g.log("codex", idEnded2, 250)
	var calls []string
	terminalLogsBeforeRecheck = func(name string) {
		calls = append(calls, name)
		if len(calls) == 2 { // the first log is already gone; the second agent wakes now
			_, err := g.h.db.ExecContext(context.Background(), "UPDATE live_sessions SET status = 'active' WHERE agent_type = 'codex'")
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() { terminalLogsBeforeRecheck = nil })
	code, out := g.call("POST", "/api/system/terminal-logs/delete-ended", map[string]string{testDashboardHeader: "1"}, map[string]any{"count": 2, "bytes": 350})
	require.Equal(t, http.StatusOK, code, out)
	require.Len(t, calls, 2, "one recheck per log")
	assert.EqualValues(t, 1, out["deleted"])
	assert.Len(t, out["failed"], 1)
	assert.False(t, g.exists(first))
	assert.True(t, g.exists(second), "the log of the agent woken after the first removal is kept")
}
