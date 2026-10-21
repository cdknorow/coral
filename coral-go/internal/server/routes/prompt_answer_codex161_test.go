package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// #58: Codex 0.161 on Windows (ConPTY), captured with a clean CODEX_HOME and one hook given with -c (the user name
// redacted): the hook review open, with option 2 selected after a digit, and the run to Codex's start screen. The
// selection moves by redrawing the two option rows, the first padded to the full 120 columns and the second begun
// with styling right after: a renderer that lets the styling cancel the pending wrap put the new "›" at the end of
// the first row, so the answer's Enter was never sent and the prompt was nonetheless reported as answered.
func TestAnswerPrompt_Codex161HookTrustOnPTY(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000000161"
	const trust = "Trust all and continue"
	setup := func(t *testing.T) (func() (int, map[string]any), *mockSessionTerminal, func(string)) {
		server, handler, term, ss := setupSessionsTestServer(t)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "codex-hooks", WorkingDir: "/tmp/test"}))
		term.addSession("codex-hooks", "/tmp/test")
		handler.terminal = ptyTerminal{term}
		fastPromptChecks(t)
		setScreen := func(s string) {
			term.mu.Lock()
			term.outputs["codex-hooks"] = s
			term.mu.Unlock()
		}
		answer := func() (int, map[string]any) {
			b, _ := json.Marshal(map[string]any{"n": 2, "label": trust, "session_id": sid, "agent_type": "codex"})
			resp, err := http.Post(server.URL+"/api/sessions/live/codex-hooks/answer-prompt", "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			var body map[string]any
			json.NewDecoder(resp.Body).Decode(&body)
			return resp.StatusCode, body
		}
		return answer, term, setScreen
	}

	t.Run("the redraw is read: option 2 is the selected one", func(t *testing.T) {
		for file, wantSelected := range map[string]int{"pty161_open.raw": 1, "pty161_selected_2.raw": 2} {
			screen, ok := parsePromptScreen(ptymanager.RenderScreen([]byte(codexHookTrust(t, file)), 120, 40))
			require.True(t, ok, file)
			require.Len(t, screen.Options, 3, file)
			for i, o := range screen.Options {
				assert.Equal(t, i+1 == wantSelected, o.Selected, "%s option %d", file, i+1)
			}
			assert.Equal(t, "Trust all and continue", screen.Options[1].Label, file)
		}
	})
	t.Run("a digit then one Enter closes the menu", func(t *testing.T) {
		answer, term, setScreen := setup(t)
		var mu sync.Mutex
		selected := 1
		setScreen(codexHookTrust(t, "pty161_open.raw"))
		term.onRaw = func(string) {
			mu.Lock()
			defer mu.Unlock()
			if keys := term.raw["codex-hooks"]; len(keys) > 0 && keys[len(keys)-1] == "2" {
				selected = 2
				setScreen(codexHookTrust(t, "pty161_selected_2.raw"))
			}
		}
		term.onEnter = func(string) {
			mu.Lock()
			defer mu.Unlock()
			if selected == 2 {
				setScreen(codexHookTrust(t, "pty161_trusted.raw"))
			}
		}
		code, body := answer()
		assert.Equal(t, http.StatusOK, code, "%v", body)
		assert.Equal(t, 1, term.enters["codex-hooks"], "one Enter confirms the selection")
	})
	t.Run("a menu whose redraw can't be told from the one before is not reported answered", func(t *testing.T) {
		answer, term, setScreen := setup(t)
		setScreen(codexHookTrust(t, "pty161_open.raw"))
		// What the screen looked like when the marker landed on the end of the first row: the same menu, an option
		// label with a stray marker after it, and no option selected.
		garbled := strings.Join([]string{
			"  Hooks need review",
			"  1 hook is new or changed.",
			"  Hooks can run outside the sandbox after you trust them.",
			"",
			"  1. Review hooks                                                                                                      ›",
			" 2. Trust all and continue",
			"  3. Continue without trusting (hooks won't run)",
			"",
			"  enter confirm · esc skip",
		}, "\n")
		term.onRaw = func(string) { setScreen(garbled) }
		code, body := answer()
		assert.Equal(t, http.StatusConflict, code, "%v", body)
		assert.Equal(t, "prompt_still_open", body["code"])
	})
	// Security's N2 on #58: the same options still drawn under a changed header (a count) are not a prompt that went.
	t.Run("the same menu under a changed header is not reported answered", func(t *testing.T) {
		answer, term, setScreen := setup(t)
		setScreen(codexHookTrust(t, "pty161_open.raw"))
		changed := strings.Replace(ptymanager.RenderScreen([]byte(codexHookTrust(t, "pty161_selected_2.raw")), 120, 40), "1 hook is new or changed.", "2 hooks are new or changed.", 1)
		term.onRaw = func(string) { setScreen(changed) }
		term.onEnter = func(string) { setScreen(changed) }
		code, body := answer()
		assert.Equal(t, http.StatusConflict, code, "%v", body)
		assert.Equal(t, "prompt_still_open", body["code"])
		assert.LessOrEqual(t, term.enters["codex-hooks"], 1, "never a second Enter")
	})
}
