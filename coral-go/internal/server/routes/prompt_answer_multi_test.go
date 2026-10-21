package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #5232: a multi-select question of Claude Code (AskUserQuestion with multiSelect) draws "N. [✔] label" rows and, under
// them, one unnumbered Submit row ("Next" when more questions follow): a digit toggles a box and answers nothing, Down
// moves the selection to Submit and Enter takes it (the layout is from the CLI's own UI source, 2.1.304).
var multiLabels = []string{"Feels like a slideshow", "Doesn't show the game", "Tone/story is off", "Looks cheap/amateur", "Type something"}

// multiScreen draws the question: checked[i] is box i+1, focus 1..5 an option and 6 the Submit row. numberedChat
// puts "Chat about this" in the numbered list after Submit, as the dashboard's card saw it.
func multiScreen(checked [5]bool, focus int, last, numberedChat bool) string {
	var b strings.Builder
	b.WriteString(" ← ☐ Trailer  ✔ Submit →\n\nWhat's not working about the trailer? (Pick everything that applies.)\n\n")
	for i, l := range multiLabels {
		ptr := " "
		if focus == i+1 {
			ptr = "❯"
		}
		box := " "
		if checked[i] {
			box = "✔"
		}
		fmt.Fprintf(&b, "%s %d. [%s] %s\n", ptr, i+1, box, l)
	}
	ptr, word := " ", "Next"
	if last {
		word = "Submit"
	}
	if focus == 6 {
		ptr = "❯"
	}
	fmt.Fprintf(&b, "%s    %s\n", ptr, word)
	b.WriteString("────────────────────────────────────────\n")
	if numberedChat {
		b.WriteString("  6. Chat about this\n")
	} else {
		b.WriteString("  Chat about this\n")
	}
	b.WriteString("\nEnter to select · ↑/↓ to navigate · Esc to cancel\n")
	return b.String()
}

func TestParseMultiSelectScreen(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		s, ok := parsePromptScreen(multiScreen([5]bool{true, true, false, true, false}, 1, true, numbered))
		require.True(t, ok, "numbered chat %v", numbered)
		assert.True(t, s.Multi)
		assert.Equal(t, multiLabels, labels(s.Options[:5]), "the label is what follows the box")
		var checked []bool
		for _, o := range s.Options[:5] {
			checked = append(checked, o.Checked)
			assert.Equal(t, "select", o.Action, "a digit toggles the box, 'Type something' too: %q", o.Label)
		}
		assert.Equal(t, []bool{true, true, false, true, false}, checked)
		require.NotNil(t, s.Submit)
		assert.Equal(t, promptSubmit{Label: "Submit", Focused: false}, *s.Submit)
		assert.Equal(t, 5, s.boxes)
		if numbered {
			assert.Equal(t, "chat", s.Options[5].Action)
		}
	}
	// the selection on the Submit row: still a prompt
	s, ok := parsePromptScreen(multiScreen([5]bool{}, 6, false, false))
	require.True(t, ok, "the pointer is on Submit")
	assert.Equal(t, promptSubmit{Label: "Next", Focused: true}, *s.Submit)
	// a plain numbered list with a line "Submit" under it is not a multi-select
	plain, ok := parsePromptScreen("Which?\n❯ 1. Red\n  2. Blue\n     Submit\n")
	require.True(t, ok)
	assert.False(t, plain.Multi)
	assert.Nil(t, plain.Submit)
}

// A stand-in for the CLI's multi-select: a digit toggles a box, Down moves the selection (to Submit after the last
// box), Enter on Submit leaves the prompt, Enter on a box toggles it.
type multiAgent struct {
	mu      sync.Mutex
	checked [5]bool
	focus   int
	done    bool
}

func (a *multiAgent) screen(term *mockSessionTerminal, name string, numbered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := "● Working on it…\n\n❯ \n"
	if !a.done {
		out = multiScreen(a.checked, a.focus, true, numbered)
	}
	term.mu.Lock()
	term.outputs[name] = out
	term.mu.Unlock()
}

func setupMulti(t *testing.T, numbered bool) (answer func(map[string]any) (int, map[string]any), term *mockSessionTerminal, a *multiAgent) {
	const sid = "00000000-0000-0000-0000-000000005232"
	server, _, term, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "multi", WorkingDir: "/tmp/test"}))
	term.addSession("multi", "/tmp/test")
	fastPromptChecks(t)
	a = &multiAgent{focus: 1}
	a.screen(term, "multi", numbered)
	term.onRaw = func(string) {
		a.mu.Lock()
		keys := term.raw["multi"]
		if k := keys[len(keys)-1]; len(k) == 1 && k[0] >= '1' && k[0] <= '5' {
			a.checked[k[0]-'1'] = !a.checked[k[0]-'1']
		}
		a.mu.Unlock()
		a.screen(term, "multi", numbered)
	}
	term.onArrow = func(string) {
		a.mu.Lock()
		if arrows := term.arrows["multi"]; arrows[len(arrows)-1] == "Down" && a.focus < 6 {
			a.focus++
		}
		a.mu.Unlock()
		a.screen(term, "multi", numbered)
	}
	term.onEnter = func(string) {
		a.mu.Lock()
		if a.focus == 6 {
			a.done = true
		} else {
			a.checked[a.focus-1] = !a.checked[a.focus-1]
		}
		a.mu.Unlock()
		a.screen(term, "multi", numbered)
	}
	answer = func(req map[string]any) (int, map[string]any) {
		req["session_id"], req["agent_type"] = sid, "claude"
		b, _ := json.Marshal(req)
		resp, err := http.Post(server.URL+"/api/sessions/live/multi/answer-prompt", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	return answer, term, a
}

func TestAnswerPrompt_MultiSelectToggleAndSubmit(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		t.Run(fmt.Sprintf("numbered chat %v", numbered), func(t *testing.T) {
			answer, term, a := setupMulti(t, numbered)
			// a toggle flips the box, answers nothing, and the reply is the screen as it is now
			code, body := answer(map[string]any{"n": 3, "label": "Tone/story is off"})
			require.Equal(t, http.StatusOK, code, "%v", body)
			assert.Equal(t, true, body["toggled"] != nil)
			assert.Equal(t, true, body["checked"])
			po, _ := body["prompt_options"].(map[string]any)
			require.NotNil(t, po)
			assert.Equal(t, true, po["multi"])
			assert.Equal(t, []string{"3"}, term.raw["multi"], "one digit, no Enter")
			assert.Equal(t, 0, term.enters["multi"])
			// and untoggle it
			code, body = answer(map[string]any{"n": 3, "label": "Tone/story is off"})
			require.Equal(t, http.StatusOK, code, "%v", body)
			assert.Equal(t, false, body["checked"])
			// Submit: Down from option 1 to the Submit row (five steps), then one Enter
			a.mu.Lock()
			a.checked = [5]bool{true, true, false, true, false}
			a.mu.Unlock()
			code, body = answer(map[string]any{"submit": true})
			require.Equal(t, http.StatusOK, code, "%v", body)
			assert.Equal(t, "Submit", body["answered"])
			assert.Equal(t, []string{"Down", "Down", "Down", "Down", "Down"}, term.arrows["multi"], "from option 1 past the other four boxes to the row under them")
			assert.Equal(t, 1, term.enters["multi"], "one Enter, once the Submit row shows selected")
			assert.True(t, a.done)
		})
	}
}

func TestAnswerPrompt_MultiSelectSubmitRefusals(t *testing.T) {
	// Submit when the screen is a different prompt: nothing is sent
	answer, term, _ := setupMulti(t, false)
	term.mu.Lock()
	term.outputs["multi"] = "Which?\n❯ 1. Red\n  2. Blue\n"
	term.mu.Unlock()
	code, body := answer(map[string]any{"submit": true})
	assert.Equal(t, http.StatusConflict, code, "%v", body)
	assert.Empty(t, term.arrows["multi"])
	assert.Equal(t, 0, term.enters["multi"])

	// the selection never reaches Submit (the terminal ignores Down): no Enter is sent
	answer, term, _ = setupMulti(t, false)
	term.onArrow = func(string) {}
	code, body = answer(map[string]any{"submit": true})
	assert.Equal(t, http.StatusConflict, code, "%v", body)
	assert.Equal(t, "prompt_still_open", body["code"])
	assert.Equal(t, 0, term.enters["multi"], "no Enter unless the screen shows Submit selected")

	// a toggle whose box does not flip is not ok
	answer, term, _ = setupMulti(t, false)
	term.onRaw = func(string) {}
	code, body = answer(map[string]any{"n": 2, "label": "Doesn't show the game"})
	assert.Equal(t, http.StatusConflict, code, "%v", body)
}

// Antigravity's menus take only arrows and Enter, and an Enter on a box toggles it: a multi-select screen on those
// agents is answered in the terminal, with no key sent (toggle or Submit).
func TestAnswerPrompt_MultiSelectNeverKeysArrowAgents(t *testing.T) {
	for _, agentType := range []string{"agy", "antigravity", "gemini"} {
		t.Run(agentType, func(t *testing.T) {
			const sid = "00000000-0000-0000-0000-000000005233"
			server, _, term, ss := setupSessionsTestServer(t)
			require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: "multi", WorkingDir: "/tmp/test"}))
			term.addSession("multi", "/tmp/test")
			fastPromptChecks(t)
			term.mu.Lock()
			term.outputs["multi"] = multiScreen([5]bool{true, true, false, true, false}, 1, true, false)
			term.mu.Unlock()
			for _, req := range []map[string]any{{"n": 3, "label": "Tone/story is off"}, {"n": 1, "label": "Feels like a slideshow"}, {"submit": true}} {
				req["session_id"], req["agent_type"] = sid, agentType
				b, _ := json.Marshal(req)
				resp, err := http.Post(server.URL+"/api/sessions/live/multi/answer-prompt", "application/json", bytes.NewReader(b))
				require.NoError(t, err)
				resp.Body.Close()
				assert.Equal(t, http.StatusConflict, resp.StatusCode, "%v", req)
			}
			assert.Empty(t, term.arrows["multi"], "no arrow")
			assert.Equal(t, 0, term.enters["multi"], "no Enter")
			assert.Empty(t, term.raw["multi"], "no digit")
		})
	}
}

// Submit sends at most maxMultiDowns Downs: a selection farther from Submit than that is left to the terminal.
func TestAnswerPrompt_MultiSelectSubmitCapsTheDowns(t *testing.T) {
	answer, term, _ := setupMulti(t, false)
	term.mu.Lock()
	var long strings.Builder
	long.WriteString("Pick?\n")
	for i := 1; i <= 15; i++ {
		ptr := " "
		if i == 1 {
			ptr = "❯"
		}
		fmt.Fprintf(&long, "%s %d. [ ] option %d\n", ptr, i, i)
	}
	long.WriteString("      Submit\n")
	term.outputs["multi"] = long.String()
	term.mu.Unlock()
	code, body := answer(map[string]any{"submit": true})
	assert.Equal(t, http.StatusConflict, code, "%v", body)
	assert.Empty(t, term.arrows["multi"])
	assert.Equal(t, 0, term.enters["multi"])
}
