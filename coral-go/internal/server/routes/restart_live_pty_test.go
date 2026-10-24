package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #4767: restarting a LIVE agent on a pty Coral answered 500 ("launch command delivery to <new>:0.0 failed:
// session not found"). The live path respawned the pane under its old name (pty has no rename) and typed the
// start command to a tmux-style "<new>:0.0" target that no pty session has. A pty session restarts as a pty one:
// a new terminal under the new name, the old one ended, the start command typed to the new session. Resume, which
// had the same steps, too.
func TestRestartOfALivePTYSessionStartsItUnderItsNewName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())                       // nothing the restart writes reaches the real home
	dataDir, err := os.MkdirTemp(shortTempBase(), "rp") // short: pty and socket paths have limits
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
	stub, launched := launchRecorderCLI(t, dataDir)
	for _, agentType := range []string{"claude", "agy"} {
		require.NoError(t, ss.SetSetting(ctx, "cli_path_"+agentType, stub))
	}

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() {
		// Each session's end waits up to 5 s for its shell: end them together rather than one after another.
		var wg sync.WaitGroup
		for _, sess := range pty.ListSessions() {
			wg.Add(1)
			go func(name string) { defer wg.Done(); pty.KillWithin(name, 200*time.Millisecond) }(naming.SessionName(sess.AgentType, sess.SessionID))
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

			before := launched()
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
				return ran(launched() > before, replay)
			}, 5*time.Second, 50*time.Millisecond, "the start command ran in the new session")
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
			before := launched()
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
				return ran(launched() > before, replay)
			}, 5*time.Second, 50*time.Millisecond, "the start command ran in the new session")
		})
	}
}

// ran says the start command ran: where the stand-in records its runs, that record alone (a typed line proves nothing about delivery, and Coral's own --help probe of a configured
// CLI must not count, so the stand-in ignores probes); on Windows, which has no recorder, the replay.
func ran(recorded bool, replay []byte) bool {
	if runtime.GOOS == "windows" {
		return typedStart(replay)
	}
	return recorded
}

// launchRecorderCLI is the stand-in CLI for a test that must see the start command run. The quiet launch (#5911)
// clears the screen and the replay buffer restarts there, so the typed line may be gone before a poll sees it; a
// run is evidence that stays. On Unix the stand-in appends a line to a file and launched counts the lines; on
// Windows (stubCLI's true.cmd) it counts nothing and the replay check stays the evidence.
func launchRecorderCLI(t *testing.T, dir string) (string, func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return stubCLI(t), func() int { return 0 }
	}
	marker := filepath.Join(dir, "launched")
	script := stubScript(t, filepath.Join(dir, "recorder"), "#!/bin/sh\ncase \"$1\" in --help|--version|-h|-v) exit 0;; esac\necho run >> "+marker+"\n")
	return script, func() int {
		b, _ := os.ReadFile(marker)
		return bytes.Count(b, []byte("\n"))
	}
}

// typedStart reports whether a session's replay shows its start command typed: inline, or for one over the
// inline limit (agy's, under a long TMPDIR such as macOS's /var/folders/...) the script that holds it sourced.
func typedStart(replay []byte) bool {
	// The stand-in is /usr/bin/true, or true.cmd on Windows (stubCLI); a long command is a script, coral-command-*.
	return bytes.Contains(replay, []byte("/usr/bin/true")) || bytes.Contains(replay, []byte("true.cmd")) ||
		bytes.Contains(replay, []byte("coral-command-"))
}

// spawnFails is the pty backend with a Spawn that fails, as one does at a fork limit or for a working directory
// that is gone.
type spawnFails struct{ *ptymanager.PTYBackend }

func (spawnFails) Spawn(string, string, string, string, string, uint16, uint16) error {
	return errors.New("fork/exec: resource temporarily unavailable")
}

// livePTYFixture is a pty backend, a store and a data directory for restarting live pty agents, with
// a stand-in CLI (stubCLI) as the claude CLI; every pty session is ended at the end.
func livePTYFixture(t *testing.T) (string, *store.DB, *store.SessionStore, *ptymanager.PTYBackend) {
	t.Setenv("HOME", t.TempDir())
	dataDir, err := os.MkdirTemp(shortTempBase(), "rp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() {
		var wg sync.WaitGroup
		for _, sess := range pty.ListSessions() {
			wg.Add(1)
			go func(name string) { defer wg.Done(); pty.KillWithin(name, 200*time.Millisecond) }(naming.SessionName(sess.AgentType, sess.SessionID))
		}
		wg.Wait()
		pty.Close()
	})
	return dataDir, db, ss, pty
}

// livePTYRequest registers a live claude agent on pty with its terminal running, and restarts or resumes it.
func livePTYRequest(t *testing.T, r http.Handler, ss *store.SessionStore, pty *ptymanager.PTYBackend, dataDir, route, sid string) (string, *httptest.ResponseRecorder) {
	ctx := context.Background()
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
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/"+route, bytes.NewReader(body)))
	return name, rec
}

func ptyLifecycleRouter(h *SessionsHandler) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/restart", h.Restart)
	r.Post("/api/sessions/live/{name}/resume", h.Resume)
	return r
}

// Backend's SHOULD on #4767: a restart or resume of a live pty agent whose new terminal can't start leaves the
// agent running. The old terminal was ended before the spawn, so a failed spawn left the agent dead with a 500.
func TestRestartOfALivePTYSessionWhoseSpawnFailsLeavesItRunning(t *testing.T) {
	dataDir, db, ss, pty := livePTYFixture(t)
	// Not a *PTYBackend: the restart goes the pty way because the stored session says pty.
	h := NewSessionsHandler(db, config.Load(dataDir), spawnFails{pty}, ptymanager.NewPTYSessionTerminal(pty), nil)
	r := ptyLifecycleRouter(h)
	for _, route := range []string{"restart", "resume"} {
		t.Run(route, func(t *testing.T) {
			sid := map[string]string{"restart": "4767ffff-0000-4000-8000-000000000006", "resume": "47670000-0000-4000-8000-000000000007"}[route]
			before := len(pty.ListSessions())
			name, rec := livePTYRequest(t, r, ss, pty, dataDir, route, sid)
			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "pty spawn failed")
			assert.True(t, pty.IsRunning(name), "the old terminal still runs")
			assert.Len(t, pty.ListSessions(), before+1, "no new terminal")
			ls, err := ss.GetLiveSession(context.Background(), sid)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Equal(t, "active", ls.Status, "the agent is still live in the store")
		})
	}
}

// oldKillFails is the pty backend with a Kill of one name that fails: with err, after really ending the
// terminal when gone is set (its process exited between the restart finding it and ending it).
type oldKillFails struct {
	*ptymanager.PTYBackend
	name string
	err  error
	gone bool
}

func (b *oldKillFails) Kill(name string) error {
	if name != b.name {
		return b.PTYBackend.Kill(name)
	}
	if b.gone {
		b.PTYBackend.KillWithin(name, 200*time.Millisecond)
	}
	return b.err
}

// KillWithin is the restart's way of ending the old terminal (#4842): the same failure.
func (b *oldKillFails) KillWithin(name string, grace time.Duration) error {
	if name != b.name {
		return b.PTYBackend.KillWithin(name, grace)
	}
	return b.Kill(name)
}

// #4767 follow-up (Backend's SHOULD and INFO): when the old pty terminal can't be ended, the new one is ended
// too and the request fails, leaving no orphan shell running with no agent; when it is already gone (its process
// exited just before), it counts as ended and the restart goes on.
func TestRestartOfALivePTYSessionWhoseOldTerminalCantBeEnded(t *testing.T) {
	gone := ptymanager.NewPTYBackend()
	assert.ErrorIs(t, gone.Kill("claude-nobody"), ptymanager.ErrTerminalGone, "a pty Kill of a session that isn't there says the terminal is gone")
	gone.Close()

	dataDir, db, ss, pty := livePTYFixture(t)
	n := 0
	for _, route := range []string{"restart", "resume"} {
		for _, alreadyGone := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, already gone %v", route, alreadyGone), func(t *testing.T) {
				n++
				sid := fmt.Sprintf("4767%04d-0000-4000-8000-0000000000%02d", n, 10+n)
				fake := &oldKillFails{PTYBackend: pty, name: naming.SessionName("claude", sid), gone: alreadyGone,
					err: errors.New("operation not permitted")}
				if alreadyGone {
					fake.err = fmt.Errorf("%w: session %q not found", ptymanager.ErrTerminalGone, fake.name)
				}
				h := NewSessionsHandler(db, config.Load(dataDir), fake, ptymanager.NewPTYSessionTerminal(pty), nil)
				before := map[string]bool{}
				for _, s := range pty.ListSessions() {
					before[naming.SessionName(s.AgentType, s.SessionID)] = true
				}
				name, rec := livePTYRequest(t, ptyLifecycleRouter(h), ss, pty, dataDir, route, sid)
				var started []string
				for _, s := range pty.ListSessions() {
					if k := naming.SessionName(s.AgentType, s.SessionID); !before[k] && k != name {
						started = append(started, k)
					}
				}
				if alreadyGone {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Len(t, started, 1, "the agent runs in its new terminal")
					return
				}
				assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "pty "+route+" failed")
				assert.True(t, pty.IsRunning(name), "the old terminal still runs")
				assert.Empty(t, started, "the new terminal is ended: no orphan shell")
			})
		}
	}
}

// #4842: restarting or resuming a live pty agent whose process ignores SIGTERM no longer waits for Kill's 5 s: the old terminal is given
// restartKillGrace and then force-killed. (The tester measured a restart of a real agy at 5.42 s on pty against 0.40 s on tmux; a resume,
// with no live terminal to end, took 0.46 s.) The old terminal here is a real pty process that ignores SIGTERM.
func TestRestartAndResumeOfAPTYAgentThatIgnoresSIGTERMAreNotHeldForFiveSeconds(t *testing.T) {
	dataDir, db, ss, pty := livePTYFixture(t)
	h := NewSessionsHandler(db, config.Load(dataDir), pty, ptymanager.NewPTYSessionTerminal(pty), nil)
	grace := 300 * time.Millisecond
	h.killGrace = &grace
	r := ptyLifecycleRouter(h)
	for _, route := range []string{"restart", "resume"} {
		t.Run(route, func(t *testing.T) {
			sid := map[string]string{"restart": "4842aaaa-0000-4000-8000-000000000001", "resume": "4842bbbb-0000-4000-8000-000000000002"}[route]
			name := naming.SessionName("claude", sid)
			require.NoError(t, pty.Spawn(name, "claude", dataDir, sid, "trap '' TERM HUP; exec sleep 600", 80, 24))
			time.Sleep(300 * time.Millisecond)
			onPTY := "pty"
			require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "tester", AgentType: "claude", WorkingDir: dataDir, Backend: &onPTY}))
			fields := map[string]any{"session_id": sid, "agent_type": "claude"}
			if route == "resume" {
				fields = map[string]any{"session_id": "4842eeee-0000-4000-8000-000000000005", "agent_type": "claude", "current_session_id": sid}
			}
			body, _ := json.Marshal(fields)
			rec := httptest.NewRecorder()
			start := time.Now()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/"+route, bytes.NewReader(body)))
			took := time.Since(start)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Less(t, took, 3*time.Second, "not held for Kill's 5 s")
			assert.False(t, pty.IsRunning(name), "the old terminal that ignored SIGTERM was force-killed")
			var out struct {
				SessionName string `json:"session_name"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			assert.True(t, pty.IsRunning(out.SessionName), "the new terminal runs")
		})
	}
}
