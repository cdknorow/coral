package routes

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// fgSessionTerminal is the mock terminal plus a foreground command per terminal
// name: a shell once the agent CLI has exited, the CLI while it runs (#3219).
type fgSessionTerminal struct {
	*mockSessionTerminal
	fgMu sync.Mutex
	fg   map[string]string
}

func newFGTerminal(m *mockSessionTerminal) *fgSessionTerminal {
	return &fgSessionTerminal{mockSessionTerminal: m, fg: map[string]string{}}
}

func (f *fgSessionTerminal) setFG(name, cmd string) {
	f.fgMu.Lock()
	defer f.fgMu.Unlock()
	f.fg[name] = cmd
}

// AgentShellIdle: a shell name set for the terminal means its shell is idle (the CLI
// exited); anything else means the CLI is running.
func (f *fgSessionTerminal) AgentShellIdle(_ context.Context, name, _, _ string) (bool, string, bool) {
	f.fgMu.Lock()
	defer f.fgMu.Unlock()
	c, ok := f.fg[strings.TrimSuffix(name, ".0")]
	if !ok || c == "" {
		return false, "", false
	}
	return tmux.IsShellCommand(c), c, true
}

func (f *fgSessionTerminal) sentTo(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent[name]...)
}

const exitedTestSID = "6a7b8c9d-0e1f-4a2b-8c3d-4e5f6a7b8c9d"

// Board notifications and nudges are not typed into the shell an exited CLI left.
func TestBoardSendAutomatedSkipsABareShell(t *testing.T) {
	term := newFGTerminal(newMockTerminal())
	h := &BoardHandler{terminal: term}
	ctx := context.Background()
	name := naming.SessionName("claude", exitedTestSID)
	term.addSession(name, "/tmp/test")

	term.setFG(name, "zsh")
	err := h.sendAutomated(ctx, name, "You have 1 unread message")
	assert.True(t, errors.Is(err, errAgentExited), "err = %v", err)
	assert.Empty(t, term.sentTo(name), "nothing typed into the shell")

	term.setFG(name, "node")
	require.NoError(t, h.sendAutomated(ctx, name, "You have 1 unread message"))
	assert.Equal(t, []string{"You have 1 unread message"}, term.sentTo(name), "a running agent gets it")
}

// Orchestrator's must: a 'terminal' agent's shell IS the agent.
func TestBoardSendAutomatedReachesATerminalAgent(t *testing.T) {
	term := newFGTerminal(newMockTerminal())
	h := &BoardHandler{terminal: term}
	name := naming.SessionName("terminal", exitedTestSID)
	term.addSession(name, "/tmp/test")
	term.setFG(name, "zsh")
	require.NoError(t, h.sendAutomated(context.Background(), name, "hello"))
	assert.Equal(t, []string{"hello"}, term.sentTo(name))
}

// Orchestrator's must: what the user types in the dashboard is always sent.
func TestUserSendIsNotBlockedWhenTheAgentExited(t *testing.T) {
	server, handler, mock, ss := setupSessionsTestServer(t)
	term := newFGTerminal(mock)
	handler.terminal = term
	name := naming.SessionName("claude", exitedTestSID)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: exitedTestSID, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	mock.addSession(name, "/tmp/test")
	term.setFG(name, "zsh")

	body := bytes.NewBufferString(`{"command": "claude --resume", "agent_type": "claude", "session_id": "` + exitedTestSID + `"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/send", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"claude --resume"}, term.sentTo(name), "user input is sent as typed")
}

// Wake of an agent whose terminal is still there but at a shell prompt resumes the
// agent in that shell; before, it only cleared the sleeping flag and nothing ran.
func TestWakeResumesAnExitedAgentInItsShell(t *testing.T) {
	_, handler, mock, ss := setupSessionsTestServer(t)
	term := newFGTerminal(mock)
	handler.terminal = term
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{sessionID}/wake", handler.WakeSession)
	srv := httptestServer(t, r)

	name := naming.SessionName("claude", exitedTestSID)
	wd := t.TempDir()
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: exitedTestSID, AgentType: "claude", AgentName: "repo", WorkingDir: wd, IsSleeping: 1}))
	require.NoError(t, mock.CreateSession(context.Background(), name, wd))
	term.setFG(name, "zsh")

	postArchiveOK(t, srv+"/api/sessions/live/"+exitedTestSID+"/wake")
	launched := term.sentTo(name + ".0")
	require.Len(t, launched, 1, "the resume command was typed into the idle shell")
	assert.Contains(t, launched[0], exitedTestSID, "it resumes this agent's session")
	s, _ := archFlags(t, ss, exitedTestSID)
	assert.Equal(t, 0, s, "awake")
}

func TestWakeLeavesARunningAgentAlone(t *testing.T) {
	_, handler, mock, ss := setupSessionsTestServer(t)
	term := newFGTerminal(mock)
	handler.terminal = term
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{sessionID}/wake", handler.WakeSession)
	srv := httptestServer(t, r)

	name := naming.SessionName("claude", exitedTestSID)
	wd := t.TempDir()
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: exitedTestSID, AgentType: "claude", AgentName: "repo", WorkingDir: wd, IsSleeping: 1}))
	require.NoError(t, mock.CreateSession(context.Background(), name, wd))
	term.setFG(name, "node") // marked sleeping by mistake, but the CLI is running

	postArchiveOK(t, srv+"/api/sessions/live/"+exitedTestSID+"/wake")
	assert.Empty(t, term.sentTo(name+".0"), "nothing typed into a running agent")
}

func httptestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
