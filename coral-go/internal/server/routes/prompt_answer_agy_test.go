package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// Antigravity CLI 1.4.2's login menu at launch (#4577), captured with a clean HOME (no account): numbered
// options, but a digit leaves the selection where it was ("↑/↓ Navigate · enter Select"); Down moves it and
// Enter confirms, opening the next menu. The .txt files are tmux capture-pane text, the .raw files the PTY's
// raw output for the same run (each up to that point).
func agyMenu(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/agy_menus/" + name)
	require.NoError(t, err)
	return string(b)
}

func TestAnswerPrompt_AgyNavigatesWithArrows(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000004577"
	for _, agentType := range []string{"agy", "gemini"} {
		for _, backend := range []string{"tmux", "pty"} {
			t.Run(agentType+"/"+backend, func(t *testing.T) {
				server, handler, term, ss := setupSessionsTestServer(t)
				require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: "agy-login", WorkingDir: "/tmp/test"}))
				term.addSession("agy-login", "/tmp/test")
				if backend == "pty" {
					handler.terminal = ptyTerminal{term}
				}
				fastPromptChecks(t)
				file := func(n string) string {
					if backend == "pty" {
						return agyMenu(t, "pty_"+n+".raw")
					}
					return agyMenu(t, n+".txt")
				}
				set := func(s string) {
					term.mu.Lock()
					term.outputs["agy-login"] = s
					term.mu.Unlock()
				}
				// A stand-in for the menu as agy runs it: a digit does nothing, Down selects option 2, and only
				// Enter on option 2 opens the next menu.
				set(file("login_open"))
				onTwo := false
				term.onArrow = func(string) {
					if a := term.arrows["agy-login"]; a[len(a)-1] == "Down" {
						onTwo = true
						set(file("login_sel2"))
					}
				}
				term.onEnter = func(string) {
					if onTwo {
						set(file("login_after"))
					}
				}
				b, _ := json.Marshal(map[string]any{"n": 2, "label": "Use a Google Cloud project", "session_id": sid, "agent_type": agentType})
				resp, err := http.Post(server.URL+"/api/sessions/live/agy-login/answer-prompt", "application/json", bytes.NewReader(b))
				require.NoError(t, err)
				defer resp.Body.Close()
				var body map[string]any
				json.NewDecoder(resp.Body).Decode(&body)
				assert.Equal(t, http.StatusOK, resp.StatusCode, "%v", body)
				assert.Empty(t, term.raw["agy-login"], "no digit: agy ignores it")
				assert.Equal(t, []string{"Down"}, term.arrows["agy-login"])
				assert.Equal(t, 1, term.enters["agy-login"], "one Enter, once option 2 shows selected")
				// The menu that was answered is gone: the next one is up.
				term.mu.Lock()
				out := term.outputs["agy-login"]
				term.mu.Unlock()
				if backend == "pty" {
					out = ptymanager.RenderScreen([]byte(out), 120, 40)
				}
				next, ok := parsePromptScreen(out)
				require.True(t, ok)
				assert.Equal(t, []string{"Continue with Google Cloud", "Use advanced SSO config"}, labels(next.Options))
			})
		}
	}
}

// The login menu reads as a numbered prompt on both backends (the PTY's rendered screen as tmux's text).
func TestParseAgyLoginMenu(t *testing.T) {
	for _, screen := range []string{agyMenu(t, "login_open.txt"), ptymanager.RenderScreen([]byte(agyMenu(t, "pty_login_open.raw")), 120, 40)} {
		s, ok := parsePromptScreen(screen)
		require.True(t, ok)
		assert.Equal(t, "Select login method:", s.Question)
		assert.Equal(t, []string{"Google OAuth", "Use a Google Cloud project"}, labels(s.Options))
		assert.True(t, s.Options[0].Selected)
		assert.False(t, s.Unnumbered)
	}
	assert.False(t, navigatesByArrows("claude"))
	assert.False(t, navigatesByArrows("codex"))
}

// agy's trust menu (unnumbered, "↑/↓ Navigate · enter Confirm", its status line below) and its permission menu
// (numbered), from the 1.3.5 corpus the gate reads (internal/ptymanager/testdata/screens/agy), read as prompts
// (#4577).
func TestParseAgyTrustAndPermissionMenus(t *testing.T) {
	sgr := regexp.MustCompile("\x1b\\[[0-9;]*m")
	read := func(name string) string {
		b, err := os.ReadFile("../../ptymanager/testdata/screens/agy/1.3.5/" + name)
		require.NoError(t, err)
		return sgr.ReplaceAllString(string(b), "")
	}
	trust, ok := parsePromptScreen(read("trust.ansi"))
	require.True(t, ok, "the trust menu is a prompt")
	assert.True(t, trust.Unnumbered)
	assert.Equal(t, []string{"Yes, I trust this folder", "No, exit"}, labels(trust.Options))
	assert.True(t, trust.Options[0].Selected)
	assert.Contains(t, trust.Question, "Do you trust the contents of this project?")

	perm, ok := parsePromptScreen(read("permission_menu.ansi"))
	require.True(t, ok, "the permission menu is a prompt")
	assert.False(t, perm.Unnumbered)
	assert.Equal(t, "Run this command?", perm.Question)
	require.Len(t, perm.Options, 4)
	assert.Equal(t, "Yes, run command", perm.Options[0].Label)
	assert.Equal(t, "No, cancel", perm.Options[3].Label)
}

// agy 1.4.2's mid-turn permission menu, captured by the Antigravity CLI tester (tmux text, redacted): open,
// option 2 selected after Down, and the agent at work after Enter. Answered from Chat with Down then one Enter
// (agy ignores a digit), and the menu is gone afterwards.
func TestAnswerPrompt_AgyPermissionMenu142(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000004578"
	server, _, term, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "agy", AgentName: "agy-perm", WorkingDir: "/tmp/test"}))
	term.addSession("agy-perm", "/tmp/test")
	fastPromptChecks(t)
	set := func(s string) {
		term.mu.Lock()
		term.outputs["agy-perm"] = s
		term.mu.Unlock()
	}
	open := agyMenu(t, "perm142_open.txt")
	s, ok := parsePromptScreen(open)
	require.True(t, ok)
	assert.Equal(t, "Run this command?", s.Question)
	require.Len(t, s.Options, 4)
	set(open)
	onTwo := false
	term.onArrow = func(string) { onTwo = true; set(agyMenu(t, "perm142_sel2.txt")) }
	term.onEnter = func(string) {
		if onTwo {
			set(agyMenu(t, "perm142_after.txt"))
		}
	}
	b, _ := json.Marshal(map[string]any{"n": 2, "label": s.Options[1].Label, "session_id": sid, "agent_type": "agy"})
	resp, err := http.Post(server.URL+"/api/sessions/live/agy-perm/answer-prompt", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "%v", body)
	assert.Empty(t, term.raw["agy-perm"])
	assert.Equal(t, []string{"Down"}, term.arrows["agy-perm"])
	assert.Equal(t, 1, term.enters["agy-perm"])
	_, still := parsePromptScreen(agyMenu(t, "perm142_after.txt"))
	assert.False(t, still, "the menu is gone")
}
