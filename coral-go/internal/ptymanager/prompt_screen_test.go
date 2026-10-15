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
	assert.True(t, InputBoxKnown("agy"), "agy's \"> \" box (#3478)")
	assert.False(t, InputBoxKnown("pi"), "an agent whose box Coral can't recognise")
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

// Antigravity, captured read-only from live sessions (#3472, testdata/agy_*.txt): while
// it works it draws a spinner line and "esc to cancel" under its input box. Sends
// are still held there (the footer), but the reason is busy, not a prompt for the
// user, so it never raises Needs input. A footer with no spinner stays a footer; a
// real menu stays a prompt even with a spinner on screen.
func TestAntigravityBusyFooterIsBusyNotAPrompt(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		require.NoError(t, err)
		return string(b)
	}
	busy := read("agy_busy.txt")
	open, why := PromptOnScreen(busy)
	assert.True(t, open, "still held while it works")
	assert.True(t, strings.HasPrefix(why, busyWhy), why)
	assert.False(t, isPromptHold(why), "busy is not a prompt")
	// The other spinner lines agy draws (the Orchestrator's coral-hub data).
	for _, spin := range []string{"⠋  Generating...", "⠙  Running command...", "⠹  Running command... (12s)", "⠸  Running command…"} {
		open, why := PromptOnScreen(strings.Replace(busy, "⣷  Working...", spin, 1))
		assert.True(t, open, spin)
		assert.True(t, strings.HasPrefix(why, busyWhy), "%s: %s", spin, why)
	}
	// Without the spinner it is a footer, as before; a reply line ending in "..." is
	// no spinner.
	for _, line := range []string{"  I checked the logs...", "Working..."} {
		open, why = PromptOnScreen(strings.Replace(busy, "⣷  Working...", line, 1))
		assert.True(t, open)
		assert.True(t, strings.HasPrefix(why, footerWhy), "%q: %s", line, why)
	}

	for _, f := range []string{"agy_idle.txt", "agy_queued.txt"} {
		open, why := PromptOnScreen(read(f))
		assert.False(t, open, "%s: %s", f, why)
	}
	assert.True(t, InputBoxOnScreen(read("agy_idle.txt")), "agy's idle box")

	footer := strings.TrimSpace(busy[strings.LastIndex(strings.TrimRight(busy, "\n"), "\n")+1:])
	menu := "⣷  Working...\n  Allow this command?\n  > 1. Allow once\n    2. Deny\n" + footer + "\n"
	open, why = PromptOnScreen(menu)
	assert.True(t, open)
	assert.True(t, isPromptHold(why), "a menu is a prompt whatever else is on screen: %s", why)

	for _, f := range []string{"codex_edit.txt", "codex_exec.txt"} { // their footer ends "esc to cancel"
		b, err := os.ReadFile(filepath.Join("..", "server", "routes", "testdata", "auto_accept", f))
		require.NoError(t, err)
		open, why := PromptOnScreen(string(b))
		assert.True(t, open, f)
		assert.True(t, isPromptHold(why), "%s: %s", f, why)
	}
}
