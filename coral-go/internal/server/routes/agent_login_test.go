package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// loginScreenTerminal is the mock terminal with a screen the test sets.
type loginScreenTerminal struct {
	*mockSessionTerminal
	mu     sync.Mutex
	screen string
	reads  int
}

func (s *loginScreenTerminal) AgentScreen(context.Context, string, string, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.screen, nil
}

func (s *loginScreenTerminal) set(screen string) {
	s.mu.Lock()
	s.screen = screen
	s.mu.Unlock()
}

func corpusScreen(t *testing.T, cli, version, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "ptymanager", "testdata", "screens", cli, version, name))
	require.NoError(t, err)
	return sgrStrip(string(b))
}

// sgrStrip drops colour codes: the gate's screen read is plain text (capture-pane -p).
func sgrStrip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';' || s[j] == ':') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// #3830: a Claude agent waiting for a sign-in. Before it has a transcript, its login screen says
// so; after, the transcript's API error does (the screen is no longer read). The chat reply
// carries needs_login and never the usage-limit notice while a login is pending, even with
// "usage limit" in the terminal's last lines (the old check fired on exactly that); the
// session's state is a request of kind login. A rate_limit error gives the CLI's own message.
func TestClaudeNeedsLoginAndUsageLimit(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	server, h, mock, ss := setupSessionsTestServer(t)
	term := &loginScreenTerminal{mockSessionTerminal: mock}
	h.terminal = term
	ctx := context.Background()
	sid := "eeeeeeee-3830-4000-8000-000000000001"
	name := naming.SessionName("claude", sid)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "signin", WorkingDir: "/tmp/project"}))
	// The terminal's log mentions a usage limit (the login method menu's own words are near it).
	require.NoError(t, os.WriteFile(naming.LogFile(h.cfg.LogDir, "claude", sid),
		[]byte("Claude account with subscription \u00b7 Pro, Max, Team, or Enterprise\nWhen a usage limit stops your session...\n"), 0o600))

	type chatReply struct {
		NeedsLogin  bool    `json:"needs_login"`
		LoginReason *string `json:"login_reason"`
		LimitNotice string  `json:"limit_notice"`
	}
	chat := func() chatReply {
		t.Helper()
		h.loginScreens = loginScreens{} // a fresh screen read each time
		resp, err := http.Get(server.URL + "/api/sessions/live/" + name + "/chat?session_id=" + sid + "&agent_type=claude&working_directory=/tmp/project&after=0&limit=10")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var r chatReply
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
		return r
	}
	state := func() SessionState {
		h.loginScreens = loginScreens{}
		return h.deriveSingleSessionState(ctx, sid, 0)
	}

	// No transcript: the screen decides. The OAuth step and the login method menu are logins.
	for _, screen := range []string{"login_oauth_code.ansi", "login_method.ansi"} {
		term.set(corpusScreen(t, "claude", "2.1.301", screen))
		r := chat()
		assert.True(t, r.NeedsLogin, screen)
		assert.Empty(t, r.LimitNotice, "%s: no usage-limit notice on a login screen", screen)
		st := state()
		assert.True(t, st.NeedsLogin && st.NeedsInput, screen)
		assert.Equal(t, "login", st.WaitingKind, screen)
	}
	term.set(corpusScreen(t, "claude", "2.1.300", "idle.ansi"))
	r := chat()
	assert.False(t, r.NeedsLogin, "an idle screen")
	assert.Empty(t, r.LimitNotice, "\"usage limit\" in the terminal's text is not the CLI's limit error")
	assert.False(t, state().NeedsLogin)

	// The transcript: the CLI's authentication error (the captured 2.1.301 lines).
	fixture, err := os.ReadFile(filepath.Join("..", "..", "jsonl", "testdata", "claude-2.1.301", "auth_invalid_api_key.jsonl"))
	require.NoError(t, err)
	dir := filepath.Join(projects, "-tmp-project")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	transcript := filepath.Join(dir, sid+".jsonl")
	require.NoError(t, os.WriteFile(transcript, fixture, 0o600))
	term.set(corpusScreen(t, "claude", "2.1.301", "login_oauth_code.ansi"))
	reads := term.reads
	r = chat()
	assert.True(t, r.NeedsLogin)
	require.NotNil(t, r.LoginReason)
	assert.Equal(t, "Invalid API key \u00b7 Fix external API key", *r.LoginReason)
	assert.Empty(t, r.LimitNotice)
	st := state()
	assert.True(t, st.NeedsLogin)
	assert.Equal(t, "Invalid API key \u00b7 Fix external API key", st.WaitingSummary)
	assert.Equal(t, reads, term.reads, "with a transcript, the screen is not read")

	appendLine := func(line string) {
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(line + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	// The user signed in and wrote again: cleared.
	appendLine(`{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-10-16T10:12:00.000Z"}`)
	r = chat()
	assert.False(t, r.NeedsLogin)
	assert.Nil(t, r.LoginReason)
	assert.False(t, state().NeedsLogin)

	// A reply that talks about limits and /login is neither.
	appendLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your usage limit? Please run /login."}]},"timestamp":"2026-10-16T10:12:01.000Z"}`)
	r = chat()
	assert.False(t, r.NeedsLogin)
	assert.Empty(t, r.LimitNotice)

	// The CLI's rate-limit error: the notice is its own message.
	limit := strings.Replace(strings.Replace(strings.Split(strings.TrimSpace(string(fixture)), "\n")[1],
		`"error":"authentication_failed"`, `"error":"rate_limit"`, 1),
		"Invalid API key \u00b7 Fix external API key", "You've hit your limit \u00b7 resets 3pm (UTC)", 1)
	appendLine(limit)
	r = chat()
	assert.False(t, r.NeedsLogin)
	assert.Equal(t, "You've hit your limit \u00b7 resets 3pm (UTC)", r.LimitNotice)
	assert.False(t, state().NeedsInput, "a usage limit is not a request")
}

// #3830: Codex's limit notice is its exact message at the end of the terminal; its idle screen's
// "You have 1 usage limit reset available" (a real 0.155.1 capture) is not one.
func TestCodexUsageLimitText(t *testing.T) {
	idle := strings.Split(strings.TrimRight(corpusScreen(t, "codex", "0.155.1", "idle.ansi"), "\n"), "\n")
	require.Contains(t, strings.Join(idle, "\n"), "usage limit reset available", "the capture has the near miss")
	assert.Empty(t, codexLimitText(map[string]any{"recent_lines": idle}))
	hit := append(append([]string{}, idle...), "\u25a0 You've hit your usage limit. Upgrade to Plus to continue using Codex.", "")
	assert.Equal(t, "You've hit your usage limit. Upgrade to Plus to continue using Codex.", codexLimitText(map[string]any{"recent_lines": hit}))
	old := append(append([]string{}, hit...), strings.Split(strings.Repeat("more output\n", codexLimitLines), "\n")...)
	assert.Empty(t, codexLimitText(map[string]any{"recent_lines": old}), "scrolled away once Codex runs again")
	assert.Equal(t, "Claude AI usage limit reached", claudeLimitText("Claude AI usage limit reached|1760000000"))
}
