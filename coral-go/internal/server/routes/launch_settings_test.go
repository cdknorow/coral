package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// cancelAfterTerminal ends a request's context once n terminal sessions have been created: a
// client that hangs up in the middle of a team launch.
type cancelAfterTerminal struct {
	*mockSessionTerminal
	mu     sync.Mutex
	made   int
	n      int
	cancel context.CancelFunc
}

func (c *cancelAfterTerminal) CreateSession(ctx context.Context, name, workDir string) error {
	err := c.mockSessionTerminal.CreateSession(ctx, name, workDir)
	c.mu.Lock()
	c.made++
	if c.made == c.n {
		c.cancel()
	}
	c.mu.Unlock()
	return err
}

// #4350: a team launch's agents all start on the CLI Settings > CLI paths names, also when the
// request's context ends partway (a client that times out or hangs up): the settings were read
// with that context, so the later agents read none and started the CLI found on PATH.
func TestTeamLaunchUsesTheConfiguredCLIForEveryAgent(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	mock := newMockTerminal()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term := &cancelAfterTerminal{mockSessionTerminal: mock, n: 2, cancel: cancel}
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, term, bs)

	exe := filepath.Join(t.TempDir(), "mock-claude")
	if runtime.GOOS == "windows" {
		exe += ".exe" // a configured path that is not a program is refused (#115)
	}
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, handler.ss.SetSetting(context.Background(), "cli_path_claude", exe))

	const agents = 20
	var list []map[string]any
	for i := 0; i < agents; i++ {
		list = append(list, map[string]any{"name": fmt.Sprintf("Agent %d", i), "prompt": "work"})
	}
	b, _ := json.Marshal(map[string]any{"board_name": "big", "working_dir": t.TempDir(), "agents": list})
	rec := httptest.NewRecorder()
	handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)).WithContext(ctx))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Error(t, ctx.Err(), "the request's context ended partway, as the client hanging up does")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	launched, real := 0, 0
	for _, cmds := range mock.sent {
		for _, c := range cmds {
			if strings.Contains(c, exe) {
				launched++
			} else if strings.Contains(c, "--session-id") || strings.Contains(c, "claude ") {
				real++
			}
		}
	}
	assert.Equal(t, agents, launched, "every agent starts on the configured CLI")
	assert.Zero(t, real, "none starts the CLI found on PATH")
}

// A launch whose settings cannot be read fails with an error and starts nothing: it never falls
// back to the CLI on PATH (#4350).
func TestLaunchFailsWhenSettingsCannotBeRead(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	mock := newMockTerminal()
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, mock, bs)
	require.NoError(t, db.Close()) // every read now fails

	b, _ := json.Marshal(map[string]any{"board_name": "x", "working_dir": t.TempDir(), "agents": []map[string]any{{"name": "A", "prompt": "p"}}})
	rec := httptest.NewRecorder()
	handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "settings could not be read")

	_, err = handler.launchSession(context.Background(), launchSpec{WorkDir: t.TempDir(), AgentType: "claude", DisplayName: "A"})
	assert.Error(t, err)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Empty(t, mock.sent, "nothing was typed into any terminal")
}

// Restart, resume and wake build an agent command from the settings too: with the settings
// unreadable (the table gone, which no other read here touches) each answers an error and types
// nothing into any terminal, instead of starting the CLI found on PATH (#4350).
func TestRestartResumeAndWakeFailWhenSettingsCannotBeRead(t *testing.T) {
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	wd := t.TempDir()
	const sid = "11111111-2222-3333-4444-555555555555"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "sleepy", AgentType: "claude", WorkingDir: wd, SessionID: sid}))
	require.NoError(t, ss.SetSessionSleeping(ctx, sid, true))
	_, err := handler.db.ExecContext(ctx, `DROP TABLE user_settings`)
	require.NoError(t, err)

	post := func(path, body string) (int, string) {
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewBufferString(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	code, body := post("/api/sessions/live/sleepy/restart", `{"session_id": "`+sid+`", "agent_type": "claude"}`)
	assert.Equal(t, http.StatusServiceUnavailable, code, "restart: %s", body)
	assert.Contains(t, body, "settings could not be read")
	code, body = post("/api/sessions/live/sleepy/resume", `{"session_id": "`+sid+`", "agent_type": "claude", "current_session_id": "`+sid+`"}`)
	assert.Equal(t, http.StatusServiceUnavailable, code, "resume: %s", body)
	assert.Contains(t, body, "settings could not be read")

	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	err = handler.wakeExistingSession(ctx, ls)
	assert.ErrorIs(t, err, errLaunchSettings, "wake")

	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.sent, "nothing was typed into any terminal")
}
