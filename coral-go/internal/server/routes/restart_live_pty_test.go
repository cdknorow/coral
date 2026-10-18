package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4767: restarting a LIVE agent on a pty Coral answered 500 ("launch command delivery to <new>:0.0 failed:
// session not found"). The live path respawned the pane under its old name (pty has no rename) and typed the
// start command to a tmux-style "<new>:0.0" target that no pty session has. A pty session restarts as a pty one:
// a new terminal under the new name, the old one ended, the start command typed to the new session. Resume, which
// had the same steps, too.
func TestRestartOfALivePTYSessionStartsItUnderItsNewName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())              // nothing the restart writes reaches the real home
	dataDir, err := os.MkdirTemp("/tmp", "rp") // short: pty and socket paths have limits
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	// The CLI the restart types is a harmless stand-in: never a real agent CLI in a test.
	for _, agentType := range []string{"claude", "agy"} {
		require.NoError(t, ss.SetSetting(ctx, "cli_path_"+agentType, "/usr/bin/true"))
	}

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() {
		// Each session's end waits up to 5 s for its shell: end them together rather than one after another.
		var wg sync.WaitGroup
		for _, sess := range pty.ListSessions() {
			wg.Add(1)
			go func(name string) { defer wg.Done(); pty.Kill(name) }(naming.SessionName(sess.AgentType, sess.SessionID))
		}
		wg.Wait()
		pty.Close()
	})
	h := NewSessionsHandler(db, cfg, pty, ptymanager.NewPTYSessionTerminal(pty), nil)
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/restart", h.Restart)
	r.Post("/api/sessions/live/{name}/resume", h.Resume)

	for _, agentType := range []string{"claude", "agy"} {
		t.Run(agentType, func(t *testing.T) {
			sid := map[string]string{"claude": "4767aaaa-0000-4000-8000-000000000001", "agy": "4767bbbb-0000-4000-8000-000000000002"}[agentType]
			name := naming.SessionName(agentType, sid)
			require.NoError(t, pty.Spawn(name, agentType, dataDir, sid, "", 80, 24)) // the live agent's terminal (a shell)
			onPTY := "pty"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
				SessionID: sid, AgentName: "tester", AgentType: agentType, WorkingDir: dataDir, Backend: &onPTY,
			}))

			body, _ := json.Marshal(map[string]any{"session_id": sid, "agent_type": agentType})
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/restart", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var out struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			assert.Equal(t, naming.SessionName(agentType, out.SessionID), out.SessionName)
			assert.True(t, pty.IsRunning(out.SessionName), "the new session runs under its new name")
			assert.False(t, pty.IsRunning(name), "the old terminal is ended")
			ls, err := ss.GetLiveSession(ctx, out.SessionID)
			require.NoError(t, err)
			require.NotNil(t, ls, "the store has the new session")
			assert.Eventually(t, func() bool {
				replay, _ := pty.Replay(out.SessionName)
				return typedStart(replay)
			}, 5*time.Second, 50*time.Millisecond, "the start command was typed into the new session")
		})
	}

	// Resume takes the same steps, and had the same "<new>:0.0" target for a live pty pane.
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "resume, live", false: "resume, no terminal"}[live], func(t *testing.T) {
			sid := map[bool]string{true: "4767cccc-0000-4000-8000-000000000003", false: "4767dddd-0000-4000-8000-000000000004"}[live]
			name := naming.SessionName("claude", sid)
			if live {
				require.NoError(t, pty.Spawn(name, "claude", dataDir, sid, "", 80, 24))
			}
			onPTY := "pty"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
				SessionID: sid, AgentName: "tester", AgentType: "claude", WorkingDir: dataDir, Backend: &onPTY,
			}))
			resumeID := "4767eeee-0000-4000-8000-000000000005" // the conversation to resume
			body, _ := json.Marshal(map[string]any{"session_id": resumeID, "agent_type": "claude", "current_session_id": sid})
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/resume", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var out struct {
				SessionName string `json:"session_name"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			require.NotEmpty(t, out.SessionName, rec.Body.String())
			assert.True(t, pty.IsRunning(out.SessionName), "the resumed agent runs under its new name")
			assert.False(t, pty.IsRunning(name), "the old terminal is ended")
			assert.Eventually(t, func() bool {
				replay, _ := pty.Replay(out.SessionName)
				return typedStart(replay)
			}, 5*time.Second, 50*time.Millisecond, "the start command was typed into the new session")
		})
	}
}

// typedStart reports whether a session's replay shows its start command typed: inline, or for one over the
// inline limit (agy's, under a long TMPDIR such as macOS's /var/folders/...) the script that holds it sourced.
func typedStart(replay []byte) bool {
	return bytes.Contains(replay, []byte("/usr/bin/true")) || bytes.Contains(replay, []byte("/coral-command-"))
}

// spawnFails is the pty backend with a Spawn that fails, as one does at a fork limit or for a working directory
// that is gone.
type spawnFails struct{ *ptymanager.PTYBackend }

func (spawnFails) Spawn(string, string, string, string, string, uint16, uint16) error {
	return errors.New("fork/exec: resource temporarily unavailable")
}

// Backend's SHOULD on #4767: a restart or resume of a live pty agent whose new terminal can't start leaves the
// agent running. The old terminal was ended before the spawn, so a failed spawn left the agent dead with a 500.
func TestRestartOfALivePTYSessionWhoseSpawnFailsLeavesItRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir, err := os.MkdirTemp("/tmp", "rp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, "cli_path_claude", "/usr/bin/true"))

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() {
		var wg sync.WaitGroup
		for _, sess := range pty.ListSessions() {
			wg.Add(1)
			go func(name string) { defer wg.Done(); pty.Kill(name) }(naming.SessionName(sess.AgentType, sess.SessionID))
		}
		wg.Wait()
		pty.Close()
	})
	// Not a *PTYBackend: the restart goes the pty way because the stored session says pty.
	h := NewSessionsHandler(db, cfg, spawnFails{pty}, ptymanager.NewPTYSessionTerminal(pty), nil)
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/restart", h.Restart)
	r.Post("/api/sessions/live/{name}/resume", h.Resume)

	for _, route := range []string{"restart", "resume"} {
		t.Run(route, func(t *testing.T) {
			sid := map[string]string{"restart": "4767ffff-0000-4000-8000-000000000006", "resume": "47670000-0000-4000-8000-000000000007"}[route]
			name := naming.SessionName("claude", sid)
			require.NoError(t, pty.Spawn(name, "claude", dataDir, sid, "", 80, 24))
			onPTY := "pty"
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
				SessionID: sid, AgentName: "tester", AgentType: "claude", WorkingDir: dataDir, Backend: &onPTY,
			}))
			fields := map[string]any{"session_id": sid, "agent_type": "claude"}
			if route == "resume" {
				fields = map[string]any{"session_id": "4767eeee-0000-4000-8000-000000000005", "agent_type": "claude", "current_session_id": sid}
			}
			body, _ := json.Marshal(fields)
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/"+route, bytes.NewReader(body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "pty spawn failed")
			assert.True(t, pty.IsRunning(name), "the old terminal still runs")
			assert.Len(t, pty.ListSessions(), 1+map[string]int{"restart": 0, "resume": 1}[route], "no new terminal")
			ls, err := ss.GetLiveSession(ctx, sid)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Equal(t, "active", ls.Status, "the agent is still live in the store")
		})
	}
}
