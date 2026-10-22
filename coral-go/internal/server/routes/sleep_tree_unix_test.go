//go:build !windows

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #6125: putting an agent to sleep on the built-in terminal ends its whole process tree, not only its shell: the route goes through the same kill as Kill. A real agent terminal (a real pty), a child of the
// agent in a session of its own (what Codex's code-mode host is), and the sleep route itself.
func TestSleepEndsAPtyAgentsWholeTree(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to make a child in a session of its own")
	}
	dataDir, err := os.MkdirTemp(shortTempBase(), "slp")
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
	const sid = "7a1b2c3d-6125-4000-8000-000000000001"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "a", AgentType: "claude", WorkingDir: dataDir}))

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() { pty.Close() })
	h := NewSessionsHandler(db, cfg, pty, ptymanager.NewPTYSessionTerminal(pty), nil)

	pidFile := filepath.Join(dataDir, "child.pid")
	// the agent: a process that starts a child with a session of its own and waits for it
	cmd := `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); open F, ">` + pidFile + `"; print F $$; close F; exec "sleep", "600" } wait'`
	name := naming.SessionName("claude", sid)
	require.NoError(t, pty.Spawn(name, "claude", dataDir, sid, cmd, 80, 24))
	var child int
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && child == 0; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	require.Greater(t, child, 1, "the child reported its pid")
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) }) // only this recorded pid, if the test fails
	if sess, _ := unix.Getsid(child); sess != child {
		t.Fatalf("the child should lead a session of its own (session %d)", sess)
	}

	r := chi.NewRouter()
	r.Post("/api/sessions/live/{sessionID}/sleep", h.SleepSession)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+sid+"/sleep", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, 1, ls.IsSleeping, "the agent is asleep")
	assert.False(t, pty.IsRunning(name), "its terminal is gone")
	gone := false
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(child, 0) == syscall.ESRCH {
			gone = true
			break
		}
	}
	assert.True(t, gone, "the agent's child, in a session of its own, was ended with the sleep")
}
