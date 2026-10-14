package ptymanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real captures of open prompts (the auto-accept fixtures) and the screens
// Claude and Codex draw at launch: each is an open prompt (#3431).
func TestPromptOnScreenRecognisesOpenPrompts(t *testing.T) {
	dir := filepath.Join("..", "server", "routes", "testdata", "auto_accept")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		ok, why := PromptOnScreen(string(b))
		assert.True(t, ok, "%s is an open prompt", e.Name())
		assert.NotEmpty(t, why, e.Name())
	}
	for name, screen := range map[string]string{
		"claude trust folder": ` Do you trust the files in this folder?

 /Users/me/project

 Claude Code may read, write, or execute files contained in this directory.

 ❯ 1. Yes, proceed
   2. No, exit

 Enter to confirm · Esc to exit
`,
		"claude model picker": ` Select model
 Switch between Claude models.

   1. Default (recommended)
 ❯ 2. Opus
   3. Haiku
`,
		"codex sign-in menu": `  Sign in with ChatGPT to use Codex

› 1. Sign in with ChatGPT
  2. Provide your own API key

  Press Enter to continue
`,
		"agy ask_question": `  Which database should I use?

  > 1. SQLite
    2. Postgres
`,
		"ansi coloured menu": "\x1b[1m Do you want to proceed?\x1b[0m\r\n \x1b[36m❯ 1. Yes\x1b[0m\r\n   2. No\r\n",
	} {
		ok, _ := PromptOnScreen(screen)
		assert.True(t, ok, name)
	}
}

// What an agent looks like when nothing is waiting on the user: typing there
// is fine.
func TestPromptOnScreenIgnoresNormalScreens(t *testing.T) {
	for name, screen := range map[string]string{
		"claude idle input box": `⏺ Done. The tests pass.

╭──────────────────────────────────────────────╮
│ >                                            │
╰──────────────────────────────────────────────╯
  ? for shortcuts
`,
		"claude working": `⏺ Running tests…

✻ Thinking… (12s · esc to interrupt)

╭──────────────────────────────────────────────╮
│ >                                            │
╰──────────────────────────────────────────────╯
`,
		"a numbered list in a reply": `⏺ Here is the plan:
  1. Add the gate
  2. Route every sender through it
  3. Test it

╭──────────────────────────────────────────────╮
│ >                                            │
╰──────────────────────────────────────────────╯
`,
		"one option-like line typed in the box": `╭──────────────────────────────────────────────╮
│ > 1. do the thing                            │
╰──────────────────────────────────────────────╯
`,
		"codex idle": `  ▌ Ask Codex to do anything

  ⏎ send   ⌃J newline   ⌃T transcript   ⌃C quit
`,
		"an answered menu above the input box (not cleared)": " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n1\n⏺ Done.\n\n╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n",
		"empty": "",
	} {
		ok, why := PromptOnScreen(screen)
		assert.False(t, ok, "%s: %s", name, why)
	}
}

// A launch's first prompt waits for the agent's input box (#3431).
func TestInputBoxOnScreen(t *testing.T) {
	for name, tc := range map[string]struct {
		screen string
		ready  bool
	}{
		"claude idle box":   {"╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n", true},
		"claude ❯ prompt":   {"⏺ Ready.\n\n❯ \n  ⏵⏵ accept edits on\n", true},
		"codex idle":        {"  ▌ Ask Codex to do anything\n\n  ⏎ send   ⌃J newline\n", true},
		"codex › input":     {"› Ask Codex to do anything\n", true},
		"gemini idle":       {"> Type your message or @path/to/file\n", true},
		"trust folder":      {" Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit\n\n Enter to confirm · Esc to exit\n", false},
		"still starting":    {"Welcome to Claude Code!\n\n  /help for help\n", false},
		"blank":             {"", false},
		"a permission menu": {" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n", false},
	} {
		assert.Equal(t, tc.ready, InputBoxOnScreen(tc.screen), name)
	}
	assert.True(t, InputBoxKnown("claude"))
	assert.False(t, InputBoxKnown("agy"), "an agent whose box Coral can't recognise")
}

// Real Claude 2.1.299 screens (#3457 hotfix): a no-break space after the marker,
// which regexp's \s doesn't match. Its idle input box wasn't recognised, so a
// launch's first prompt (RequireInputBox) waited the whole 2-minute fallback.
func TestRealClaudeScreens(t *testing.T) {
	rule := strings.Repeat("─", 120)
	idle := rule + "\n❯ Try \"how do I log an error?\"\n" + rule + "\n  ⏸ manual mode on · ? for shortcuts · ← for agents\n"
	typed := rule + "\n❯ You have 1 unread message on the message board.\n" + rule + "\n  ⏸ manual mode on\n"
	assert.True(t, InputBoxOnScreen(idle), "Claude's idle input box")
	assert.True(t, InputBoxOnScreen(typed), "with text in it")
	open, _ := PromptOnScreen(idle)
	assert.False(t, open, "no prompt")

	menu := " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and always allow\n   3. No\n\n Esc to cancel · Tab to amend\n"
	open, _ = PromptOnScreen(menu)
	assert.True(t, open, "a menu with the no-break space is a prompt")
	assert.False(t, InputBoxOnScreen(menu), "its highlighted option isn't an input box")
}
