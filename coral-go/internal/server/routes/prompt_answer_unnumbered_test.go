package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// Claude Code 2.1.302's menus at launch whose options carry no numbers (#4543), captured with a clean HOME
// and a dummy ANTHROPIC_API_KEY (masked, paths redacted): the custom API key question, then the folder trust
// question; Up and Down move the selection and Enter confirms. The .txt files are tmux capture-pane text, the
// .raw files the PTY backend's raw output for the same run (each up to that point).
func claudeUnnumbered(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/claude_unnumbered/" + name)
	require.NoError(t, err)
	return string(b)
}

func TestParseUnnumberedMenus(t *testing.T) {
	trustQ := "Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first."
	for _, c := range []struct {
		file, question string
		labels         []string
		selected       int
	}{
		{"trust.txt", trustQ, []string{"No, exit", "Yes, I trust this folder"}, 1},
		{"trust_yes.txt", trustQ, []string{"No, exit", "Yes, I trust this folder"}, 2},
		{"apikey.txt", "Do you want to use this API key?", []string{"Yes", "No (recommended)"}, 2},
		{"apikey_yes.txt", "Do you want to use this API key?", []string{"Yes", "No (recommended)"}, 1},
	} {
		for _, backend := range []string{"tmux", "pty"} {
			screen := claudeUnnumbered(t, c.file)
			if backend == "pty" {
				screen = ptymanager.RenderScreen([]byte(claudeUnnumbered(t, "pty_"+strings.TrimSuffix(c.file, ".txt")+".raw")), 120, 40)
			}
			s, ok := parsePromptScreen(screen)
			require.True(t, ok, "%s %s", backend, c.file)
			assert.True(t, s.Unnumbered)
			assert.Equal(t, c.question, s.Question, "%s %s", backend, c.file)
			assert.Equal(t, c.labels, labels(s.Options), "%s %s", backend, c.file)
			for i, o := range s.Options {
				assert.Equal(t, i+1 == c.selected, o.Selected, "%s %s option %d", backend, c.file, i+1)
				assert.Equal(t, i+1, o.N)
				assert.Equal(t, "select", o.Action)
			}
		}
	}
	// Not menus: Claude's input box once ready, the theme picker (no menu footer: Coral leaves it to the
	// terminal), a reply with a "❯" line, and numbered prompts keep their own parse.
	for _, f := range []string{"ready.txt", "theme.txt"} {
		_, ok := parsePromptScreen(claudeUnnumbered(t, f))
		assert.False(t, ok, f)
	}
	_, ok := parsePromptScreen(ptymanager.RenderScreen([]byte(claudeUnnumbered(t, "pty_ready.raw")), 120, 40))
	assert.False(t, ok, "pty ready")
	_, ok = parsePromptScreen("Run it like this:\n❯ make test\n  make lint\n\nEnter to confirm the plan in your reply.\n")
	assert.False(t, ok, "only a menu footer line ends a menu; this one names Enter mid-sentence but has a marked line: still refused")
	s, ok := parsePromptScreen(screenPermission)
	require.True(t, ok)
	assert.False(t, s.Unnumbered)
}

func TestAnswerPrompt_ClaudeUnnumbered(t *testing.T) {
	const sid = "00000000-0000-0000-0000-00000000a5c2"
	setup := func(t *testing.T, backend string) (answer func(n int, label string) (int, map[string]any), term *mockSessionTerminal, setScreen func(string), screen func() string, base string) {
		server, handler, terminal, ss := setupSessionsTestServer(t)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-trust", WorkingDir: "/tmp/test"}))
		terminal.addSession("claude-trust", "/tmp/test")
		if backend == "pty" {
			handler.terminal = ptyTerminal{terminal}
		}
		fastPromptChecks(t)
		setScreen = func(s string) {
			terminal.mu.Lock()
			terminal.outputs["claude-trust"] = s
			terminal.mu.Unlock()
		}
		screen = func() string {
			terminal.mu.Lock()
			defer terminal.mu.Unlock()
			if backend == "pty" {
				return ptymanager.RenderScreen([]byte(terminal.outputs["claude-trust"]), 120, 40)
			}
			return terminal.outputs["claude-trust"]
		}
		base = server.URL + "/api/sessions/live/claude-trust"
		answer = func(n int, label string) (int, map[string]any) {
			b, _ := json.Marshal(map[string]any{"n": n, "label": label, "session_id": sid, "agent_type": "claude"})
			resp, err := http.Post(base+"/answer-prompt", "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			var body map[string]any
			json.NewDecoder(resp.Body).Decode(&body)
			return resp.StatusCode, body
		}
		return answer, terminal, setScreen, screen, base
	}
	file := func(backend, name string) string {
		if backend == "pty" {
			return "pty_" + name + ".raw"
		}
		return name + ".txt"
	}
	for _, backend := range []string{"tmux", "pty"} {
		t.Run(backend+"/options are read", func(t *testing.T) {
			_, _, setScreen, _, base := setup(t, backend)
			setScreen(claudeUnnumbered(t, file(backend, "trust")))
			resp, err := http.Get(base + "/chat?" + url.Values{"session_id": {sid}, "agent_type": {"claude"}, "after": {"1"}}.Encode())
			require.NoError(t, err)
			defer resp.Body.Close()
			var reply struct {
				PromptOptions promptScreen `json:"prompt_options"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&reply))
			assert.Equal(t, []string{"No, exit", "Yes, I trust this folder"}, labels(reply.PromptOptions.Options))
			assert.True(t, reply.PromptOptions.Unnumbered)
		})
		t.Run(backend+"/trusting the folder: Down, then Enter, and Claude's input box is back", func(t *testing.T) {
			answer, term, setScreen, screen, _ := setup(t, backend)
			// A stand-in for the menu as Claude runs it: Down moves the selection to Yes, and Enter on Yes
			// closes it (Enter on "No, exit" would quit).
			setScreen(claudeUnnumbered(t, file(backend, "trust")))
			onYes := false
			term.onArrow = func(string) {
				if a := term.arrows["claude-trust"]; a[len(a)-1] == "Down" {
					onYes = true
					setScreen(claudeUnnumbered(t, file(backend, "trust_yes")))
				}
			}
			term.onEnter = func(string) {
				if onYes {
					setScreen(claudeUnnumbered(t, file(backend, "ready")))
				}
			}
			code, body := answer(2, "Yes, I trust this folder")
			assert.Equal(t, http.StatusOK, code, "%v", body)
			assert.Equal(t, []string{"Down"}, term.arrows["claude-trust"])
			assert.Equal(t, 1, term.enters["claude-trust"])
			assert.Empty(t, term.raw["claude-trust"], "no digit")
			out := screen()
			open, why := ptymanager.PromptOnScreen(out)
			assert.False(t, open, "the menu is gone (%s)", why)
			assert.True(t, ptymanager.InputBoxOnScreen(out), "Claude's input box is back:\n%s", out)
		})
		t.Run(backend+"/the API key: Up to Yes, then Enter", func(t *testing.T) {
			answer, term, setScreen, _, _ := setup(t, backend)
			setScreen(claudeUnnumbered(t, file(backend, "apikey")))
			term.onArrow = func(string) { setScreen(claudeUnnumbered(t, file(backend, "apikey_yes"))) }
			term.onEnter = func(string) { setScreen(claudeUnnumbered(t, file(backend, "trust"))) } // the next menu
			code, body := answer(1, "Yes")
			assert.Equal(t, http.StatusOK, code, "%v", body)
			assert.Equal(t, []string{"Up"}, term.arrows["claude-trust"])
			assert.Equal(t, 1, term.enters["claude-trust"], "one Enter: never into the trust menu that follows")
		})
	}
	t.Run("the option already selected: Enter alone", func(t *testing.T) {
		answer, term, setScreen, _, _ := setup(t, "tmux")
		setScreen(claudeUnnumbered(t, "apikey.txt"))
		term.onEnter = func(string) { setScreen(claudeUnnumbered(t, "trust.txt")) }
		code, body := answer(2, "No (recommended)")
		assert.Equal(t, http.StatusOK, code, "%v", body)
		assert.Empty(t, term.arrows["claude-trust"])
		assert.Equal(t, 1, term.enters["claude-trust"])
	})
	t.Run("the selection doesn't move: no Enter", func(t *testing.T) {
		answer, term, setScreen, _, _ := setup(t, "tmux")
		setScreen(claudeUnnumbered(t, "trust.txt")) // Down is lost: "No, exit" stays selected
		code, body := answer(2, "Yes, I trust this folder")
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "prompt_still_open", body["code"])
		assert.Zero(t, term.enters["claude-trust"], "Enter would have chosen No, exit")
	})
	t.Run("Enter that doesn't close it is reported", func(t *testing.T) {
		answer, term, setScreen, _, _ := setup(t, "tmux")
		setScreen(claudeUnnumbered(t, "trust.txt"))
		term.onArrow = func(string) { setScreen(claudeUnnumbered(t, "trust_yes.txt")) }
		code, body := answer(2, "Yes, I trust this folder")
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "prompt_still_open", body["code"])
		assert.Equal(t, 1, term.enters["claude-trust"])
	})
}
