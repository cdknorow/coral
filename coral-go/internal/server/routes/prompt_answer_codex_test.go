package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// Codex's hook review at launch (#4543), captured from Codex CLI 0.155.1 with a clean CODEX_HOME and two
// hooks given with -c, as Coral launches it (paths redacted). On this menu a digit only moves the selection
// and Enter confirms it ("Press enter to confirm"), unlike its sign-in menu, which a digit answers. The tmux
// files are capture-pane text; the PTY files are the raw output the PTY backend keeps (the hook review drawn,
// then with option 2 selected, then the whole run to Codex's input box after Enter).
func codexHookTrust(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/codex_hook_trust/" + name)
	require.NoError(t, err)
	return string(b)
}

// ptyTerminal is the mock with the PTY backend's screen: its outputs are raw terminal output, and the
// prompt reader gets them rendered, as ptymanager.PTYSessionTerminal gives them.
type ptyTerminal struct{ *mockSessionTerminal }

func (p ptyTerminal) RenderedScreen(_ context.Context, name, _, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ptymanager.RenderScreen([]byte(p.outputs[name]), 120, 40), nil
}

func TestAnswerPrompt_CodexHookTrust(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000004543"
	const trust = "Trust all and continue"
	setup := func(t *testing.T, backend string) (answer func(n int, label string) (int, map[string]any), terminal *mockSessionTerminal, setScreen func(string), base string) {
		server, handler, term, ss := setupSessionsTestServer(t)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "codex-hooks", WorkingDir: "/tmp/test"}))
		term.addSession("codex-hooks", "/tmp/test")
		if backend == "pty" {
			handler.terminal = ptyTerminal{term}
		}
		fastPromptChecks(t)
		setScreen = func(s string) {
			term.mu.Lock()
			term.outputs["codex-hooks"] = s
			term.mu.Unlock()
		}
		base = server.URL + "/api/sessions/live/codex-hooks"
		answer = func(n int, label string) (int, map[string]any) {
			b, _ := json.Marshal(map[string]any{"n": n, "label": label, "session_id": sid, "agent_type": "codex"})
			resp, err := http.Post(base+"/answer-prompt", "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			var body map[string]any
			json.NewDecoder(resp.Body).Decode(&body)
			return resp.StatusCode, body
		}
		return answer, term, setScreen, base
	}
	files := map[string][3]string{ // open, option 2 selected, trusted
		"tmux": {"tmux_open.txt", "tmux_selected_2.txt", "tmux_trusted.txt"},
		"pty":  {"pty_open.raw", "pty_selected_2.raw", "pty_trusted.raw"},
	}
	for _, backend := range []string{"tmux", "pty"} {
		f := files[backend]
		t.Run(backend+"/options are read", func(t *testing.T) {
			_, _, setScreen, base := setup(t, backend)
			setScreen(codexHookTrust(t, f[0]))
			resp, err := http.Get(base + "/chat?" + url.Values{"session_id": {sid}, "agent_type": {"codex"}, "after": {"1"}}.Encode())
			require.NoError(t, err)
			defer resp.Body.Close()
			var reply struct {
				PromptOptions promptScreen `json:"prompt_options"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&reply))
			assert.Equal(t, "Hooks need review 2 hooks are new or changed. Hooks can run outside the sandbox after you trust them.", reply.PromptOptions.Question)
			assert.Equal(t, "Hooks need review", reply.PromptOptions.Title, "the heading, for its own line on the card (#306)")
			assert.Equal(t, []string{"Review hooks", trust, "Continue without trusting (hooks won't run)"}, labels(reply.PromptOptions.Options))
			assert.True(t, reply.PromptOptions.Options[0].Selected)
		})
		t.Run(backend+"/the menu closes and Codex's input box is back", func(t *testing.T) {
			answer, term, setScreen, _ := setup(t, backend)
			// A stand-in for the menu as Codex 0.155.1 runs it: a digit moves the selection (only option 2 is
			// captured), and only Enter on "Trust all and continue" closes it.
			var mu sync.Mutex
			selected := 1
			setScreen(codexHookTrust(t, f[0]))
			term.onRaw = func(_ string) {
				mu.Lock()
				defer mu.Unlock()
				if keys := term.raw["codex-hooks"]; len(keys) > 0 && keys[len(keys)-1] == "2" {
					selected = 2
					setScreen(codexHookTrust(t, f[1]))
				}
			}
			term.onEnter = func(string) {
				mu.Lock()
				defer mu.Unlock()
				if selected == 2 {
					setScreen(codexHookTrust(t, f[2]))
				}
			}
			code, body := answer(2, trust)
			assert.Equal(t, http.StatusOK, code, "%v", body)
			// What the user sees now: no menu, and Codex's input box.
			term.mu.Lock()
			out := term.outputs["codex-hooks"]
			term.mu.Unlock()
			if backend == "pty" {
				out = ptymanager.RenderScreen([]byte(out), 120, 40)
			}
			open, why := ptymanager.PromptOnScreen(out)
			assert.False(t, open, "the menu is gone (%s)", why)
			assert.True(t, ptymanager.InputBoxOnScreen(out), "Codex's input box is back:\n%s", out)
			assert.Equal(t, []string{"2"}, term.raw["codex-hooks"])
			assert.Equal(t, 1, term.enters["codex-hooks"], "one Enter confirms the selection")
		})
		t.Run(backend+"/a menu that stays open is not ok", func(t *testing.T) {
			answer, term, setScreen, _ := setup(t, backend)
			setScreen(codexHookTrust(t, f[0]))
			term.onRaw = func(string) { setScreen(codexHookTrust(t, f[1])) } // and Enter changes nothing
			code, body := answer(2, trust)
			assert.Equal(t, http.StatusConflict, code)
			assert.Equal(t, "prompt_still_open", body["code"])
			assert.Equal(t, 1, term.enters["codex-hooks"], "never a second Enter")
		})
	}
	t.Run("the selection never moves: no Enter, not ok", func(t *testing.T) {
		answer, term, setScreen, _ := setup(t, "tmux")
		setScreen(codexHookTrust(t, "tmux_open.txt")) // the digit is lost: option 1 stays selected
		code, body := answer(2, trust)
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "prompt_still_open", body["code"])
		assert.Zero(t, term.enters["codex-hooks"], "Enter is only for the option that was chosen")
	})
	t.Run("a menu a digit answers gets no Enter", func(t *testing.T) {
		answer, term, setScreen, _ := setup(t, "tmux")
		setScreen(screenCodexSignIn)
		term.onRaw = func(string) { setScreen("  Paste or type your API key below.\n") }
		code, body := answer(3, "Provide your own API key")
		assert.Equal(t, http.StatusOK, code, "%v", body)
		assert.Zero(t, term.enters["codex-hooks"])
	})
	t.Run("a menu a digit answers a frame late gets no Enter", func(t *testing.T) {
		answer, term, setScreen, _ := setup(t, "tmux")
		promptSettle = 150 * time.Millisecond
		setScreen(screenCodexSignIn)
		term.onRaw = func(string) {
			// The selection moves first, then the next screen is drawn: never Enter on that frame
			setScreen(strings.Replace(strings.Replace(screenCodexSignIn, "> 1.", "  1.", 1), "  3. Provide", "> 3. Provide", 1))
			time.AfterFunc(10*time.Millisecond, func() { setScreen("  Paste or type your API key below.\n") })
		}
		code, body := answer(3, "Provide your own API key")
		assert.Equal(t, http.StatusOK, code, "%v", body)
		assert.Zero(t, term.enters["codex-hooks"])
	})
	t.Run("a screen that can't be read is not a prompt that went", func(t *testing.T) {
		answer, term, setScreen, _ := setup(t, "tmux")
		setScreen(codexHookTrust(t, "tmux_open.txt"))
		term.onRaw = func(name string) {
			term.mu.Lock()
			delete(term.outputs, name) // every capture after the digit fails
			term.mu.Unlock()
		}
		code, body := answer(2, trust)
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "prompt_still_open", body["code"])
		assert.Zero(t, term.enters["codex-hooks"])
	})
}

// Claude Code's prompts are answered by their digit: a prompt that stays open is reported, and never gets
// an Enter (that could answer the next one).
func TestAnswerPrompt_ClaudeStillOpen(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000a5c1"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "claude-open", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-open", "/tmp/test")
	fastPromptChecks(t)
	terminal.outputs["claude-open"] = screenPermission
	b, _ := json.Marshal(map[string]any{"n": 1, "label": "Yes", "session_id": sid, "agent_type": "claude"})
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-open/answer-prompt", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "prompt_still_open", body["code"])
	assert.Equal(t, []string{"1"}, terminal.raw["claude-open"])
	assert.Zero(t, terminal.enters["claude-open"])
}

// The same prompt is told from the next one by its question, its labels and the lines above the question.
func TestSamePrompt(t *testing.T) {
	a, ok := parsePromptScreen(screenPermission)
	require.True(t, ok)
	moved, ok := parsePromptScreen(strings.Replace(strings.Replace(screenPermission, " ❯ 1. Yes", "   1. Yes", 1), "   4. No", " ❯ 4. No", 1))
	require.True(t, ok)
	assert.True(t, samePrompt(a, moved), "the selection moved: the same prompt")
	other, ok := parsePromptScreen(strings.Replace(screenPermission, "touch probe_file.txt", "rm probe_file.txt", -1))
	require.True(t, ok)
	assert.False(t, samePrompt(a, other), "another command: the next prompt")
}
