package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Screens captured from Claude Code 2.1.278 in a tmux pane.

const screenQuestion = `❯ Use the AskUserQuestion tool to ask me one single-select question.
────────────────────────────────────────────────────────────────────
 ☐ Color
Which color do you prefer?
❯ 1. Red
     The color red
  2. Green
     The color green
  3. Blue
     The color blue
  4. Type something.
────────────────────────────────────────────────────────────────────
  5. Chat about this
Enter to select · ↑/↓ to navigate · Esc to cancel
`

const screenPermission = `⏺ Bash(touch probe_file.txt)
  ⎿  Waiting…
────────────────────────────────────────────────────────────────────
 Bash command
   touch probe_file.txt
   Create empty probe file
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and always allow access to
      /private/tmp/cc-probe from this
      project
   3. Yes, and switch to auto mode · auto mode handles these prompts for you
   4. No
 Esc to cancel · Tab to amend
`

const screenReview = `←  ☒ Fruit  ☒ Season  ✔ Submit  →
Review your answers
 ● What is your favorite fruit?
   → Pear
 ● What is your favorite season?
   → Summer
Ready to submit your answers?
❯ 1. Submit answers
  2. Cancel
`

const screenPlan = ` Here is Claude's plan:
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Plan: create hello.txt
 Steps
 1. Create hello.txt in the working directory, containing hi.
 2. Verify it.
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
────────────────────────────────────────────────────────────────────
 Claude has written up a plan and is ready to execute. Would you like to proceed?
 ❯ 1. Yes, and use auto mode
   2. Yes, manually approve edits
   3. Tell Claude what to change
      shift+tab to approve with this feedback
 ctrl+g to edit in Vim · ~/.claude/plans/streamed-scribbling-pillow.md
`

// The trust prompt has no numbers; it is answered with arrows, so it is
// never offered as buttons.
const screenTrust = ` Quick safety check: Is this a project you created or one you trust?
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

// An idle agent whose last reply ends in a numbered list is not a prompt.
const screenIdleList = `⏺ Next steps:
  1. Rebuild the app
  2. Restart Coral
✻ Crunched for 4s
────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)
`

func labels(opts []promptOption) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Label)
	}
	return out
}

func TestParsePromptScreen(t *testing.T) {
	s, ok := parsePromptScreen(screenQuestion)
	require.True(t, ok)
	assert.Equal(t, "Which color do you prefer?", s.Question)
	assert.Equal(t, []string{"Red", "Green", "Blue", "Type something.", "Chat about this"}, labels(s.Options))
	assert.True(t, s.Options[0].Selected)
	assert.True(t, s.Options[3].FreeText && s.Options[4].FreeText, "text-entry options are flagged")
	assert.False(t, s.Options[1].FreeText)

	s, ok = parsePromptScreen(screenPermission)
	require.True(t, ok)
	assert.Equal(t, "Do you want to proceed?", s.Question)
	assert.Equal(t, []string{"Yes", "Yes, and always allow access to", "Yes, and switch to auto mode · auto mode handles these prompts for you", "No"}, labels(s.Options))

	s, ok = parsePromptScreen(screenReview)
	require.True(t, ok)
	assert.Equal(t, "Ready to submit your answers?", s.Question)
	assert.Equal(t, []string{"Submit answers", "Cancel"}, labels(s.Options))

	s, ok = parsePromptScreen(screenPlan)
	require.True(t, ok, "the plan's own numbered steps must not be mistaken for the options")
	assert.Equal(t, []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell Claude what to change"}, labels(s.Options))
	assert.True(t, s.Options[2].FreeText)
	assert.True(t, strings.HasPrefix(s.Question, "Claude has written up a plan"))

	_, ok = parsePromptScreen(screenTrust)
	assert.False(t, ok)
	_, ok = parsePromptScreen(screenIdleList)
	assert.False(t, ok, "a numbered list in a reply is not a prompt")
	_, ok = parsePromptScreen("")
	assert.False(t, ok)
}

func TestAnswerPrompt(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	terminal.addSession("claude-ask", "/tmp/test")
	base := server.URL + "/api/sessions/live/claude-ask"
	setScreen := func(s string) {
		terminal.mu.Lock()
		terminal.outputs["claude-ask"] = s
		terminal.mu.Unlock()
	}
	sentKeys := func() []string {
		terminal.mu.Lock()
		defer terminal.mu.Unlock()
		return append([]string(nil), terminal.raw["claude-ask"]...)
	}
	answer := func(n int, label string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"n": n, "label": label})
		resp, err := http.Post(base+"/answer-prompt", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	// Options are read from the screen
	setScreen(screenPermission)
	resp, err := http.Get(base + "/prompt-options?" + url.Values{}.Encode())
	require.NoError(t, err)
	var screen promptScreen
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&screen))
	resp.Body.Close()
	assert.Len(t, screen.Options, 4)

	// Answering sends the digit, but only for the option the screen shows
	code, _ := answer(4, "No")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"4"}, sentKeys())

	code, _ = answer(1, "No")
	assert.Equal(t, http.StatusConflict, code, "a label that no longer matches its number is refused")
	setScreen(screenQuestion)
	code, _ = answer(4, "Type something.")
	assert.Equal(t, http.StatusConflict, code, "text-entry options are answered in the terminal")
	setScreen(screenIdleList)
	code, _ = answer(1, "Rebuild the app")
	assert.Equal(t, http.StatusConflict, code, "nothing is sent when no prompt is open")
	assert.Equal(t, []string{"4"}, sentKeys(), "refused answers send no keys")

	code, _ = answer(0, "x")
	assert.Equal(t, http.StatusBadRequest, code)
}
