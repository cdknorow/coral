package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/tmux"
)

// agy_slash_menu.txt is agy 1.4.4's screen on Windows right after "/help" was typed (the Lead Developer's live capture, #209): the
// slash-completion list under the input box, its "tab Complete" hint and the "esc to cancel" footer. agy_trust.txt is its trust prompt as
// that capture shows it (rebuilt from the text the capture holds).
func agyScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(b)
}

func TestAgyCompletionMenuIsAFooterHoldToPromptOnScreen(t *testing.T) {
	open, why := PromptOnScreen(agyScreen(t, "agy_slash_menu.txt"))
	assert.True(t, open, "the screen is what held the Enter")
	assert.Contains(t, why, footerWhy)
}

func TestAgySlashMenuIsRecognisedForTheCommandJustTyped(t *testing.T) {
	menu := agyScreen(t, "agy_slash_menu.txt")
	assert.True(t, AgySlashMenuFor("agy", "/help", menu))
	assert.True(t, AgySlashMenuFor("antigravity", "/help", menu))
	withArgs := strings.Replace(menu, "\n> /help\n", "\n> /help me\n", 1)
	assert.True(t, AgySlashMenuFor("agy", "/help me", withArgs), "a command with arguments: its first word is the command, the box holds all of it")
	assert.False(t, AgySlashMenuFor("agy", "/help me", menu), "the input box must hold exactly the typed text")
}

func TestAgySlashMenuIsNotRecognisedWhenTheEnterWouldMeanSomethingElse(t *testing.T) {
	menu := agyScreen(t, "agy_slash_menu.txt")
	assert.False(t, AgySlashMenuFor("agy", "/he", menu), "Enter would pick /help, not the /he that was typed")
	// The box holds exactly "/he" (so the box check passes) while "/help" is highlighted: only the highlighted-command check stops this Enter.
	prefix := strings.Replace(menu, "\n> /help\n", "\n> /he\n", 1)
	require.NotEqual(t, menu, prefix)
	assert.False(t, AgySlashMenuFor("agy", "/he", prefix), "the box holds /he, the highlighted item is /help: Enter would pick /help")
	assert.False(t, AgySlashMenuFor("agy", "/model", menu), "another command than the highlighted one")
	assert.False(t, AgySlashMenuFor("agy", "hello", menu), "not a slash command")
	assert.False(t, AgySlashMenuFor("agy", "", menu))
	assert.False(t, AgySlashMenuFor("agy", "/help\nmore", menu), "a multi-line text")
	assert.False(t, AgySlashMenuFor("claude", "/help", menu), "another agent's screen is read by its own rules")
}

func TestAgyPromptsStillHold(t *testing.T) {
	trust := agyScreen(t, "agy_trust.txt")
	open, _ := PromptOnScreen(trust)
	assert.True(t, open, "the trust prompt is a prompt")
	assert.False(t, AgySlashMenuFor("agy", "/help", trust), "even while a slash command is being sent")
	busy := agyScreen(t, "agy_busy.txt")
	assert.False(t, AgySlashMenuFor("agy", "/help", busy), "agy working, 'esc to cancel' alone is not a menu")
	assert.False(t, AgySlashMenuFor("agy", "/help", permissionMenu))
	assert.False(t, AgySlashMenuFor("agy", "/help", agyScreen(t, "agy_idle.txt")))
}

// Nothing an agent prints into its transcript completes the match (Security's review of #95): the markers must be the last lines of the screen, in
// order, under an input box holding the typed text.
func TestAgySlashMenuIsMatchedByPositionNotPresence(t *testing.T) {
	menu := agyScreen(t, "agy_slash_menu.txt")
	fake := "> /help  Show available commands and keybindings\n\n  ↑/↓ Navigate · enter Select · tab Complete\nesc to cancel\n"
	trust := agyScreen(t, "agy_trust.txt")
	rule := strings.Repeat("─", 89)
	working := "⣷  Working...\n" + rule + "\n>\n" + rule + "\nesc to cancel                  Gemini 3.8 Flash · high\n"
	for name, screen := range map[string]string{
		"a real prompt, then fake menu lines below it":                 trust + fake,
		"fake menu lines, then agy's working footer":                   fake + working,
		"fake menu lines, then a question PromptOnScreen doesn't know": fake + "Proceed?\n 1. Yes\n 2. No\nesc to cancel\n",
		"the fake lines alone, with no input box":                      fake,
		"the real menu, then more output after it":                     menu + "more output\n",
		"the real menu under an input box with other text":             strings.Replace(menu, "\n> /help\n", "\n> /hel\n", 1),
	} {
		assert.False(t, AgySlashMenuFor("agy", "/help", screen), name)
	}
	assert.True(t, AgySlashMenuFor("agy", "/help", menu), "the real capture still matches")
}

// The #210 report end to end:"/help" is typed, agy opens its completion menu, and the Enter that confirms it is pressed, once. A trust
// prompt that opens in its place still withholds it.
func TestAgySlashCommandEnterIsSentThroughItsOwnCompletionMenu(t *testing.T) {
	fastGate(t)
	e := &enterAgent{fakeAgent: &fakeAgent{screen: agyScreen(t, "agy_idle.txt")}}
	name := "agy-gate-slash"
	in := AutomatedInput{Term: e, Name: name, AgentType: "agy", Text: "/help", What: "test",
		Send: func(ctx context.Context) error {
			e.mu.Lock()
			e.typed = append(e.typed, "/help")
			e.screen = agyScreen(t, "agy_slash_menu.txt") // the completion list opens as it is typed
			e.mu.Unlock()
			if !tmux.EnterAllowed(ctx) {
				return tmux.ErrEnterWithheld
			}
			e.mu.Lock()
			e.typed = append(e.typed, "<Enter>")
			e.mu.Unlock()
			return nil
		}}
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "the Enter is not held")
	assert.Equal(t, []string{"/help", "<Enter>"}, e.sent())
}

func TestAgyTrustPromptOpeningBeforeTheEnterStillWithholdsIt(t *testing.T) {
	fastGate(t)
	e := &enterAgent{fakeAgent: &fakeAgent{screen: agyScreen(t, "agy_idle.txt")}}
	name := "agy-gate-trust"
	in := AutomatedInput{Term: e, Name: name, AgentType: "agy", Text: "/help", What: "test",
		Send: func(ctx context.Context) error {
			e.mu.Lock()
			e.typed = append(e.typed, "/help")
			e.screen = agyScreen(t, "agy_trust.txt")
			e.mu.Unlock()
			if !tmux.EnterAllowed(ctx) {
				return tmux.ErrEnterWithheld
			}
			e.mu.Lock()
			e.typed = append(e.typed, "<Enter>")
			e.mu.Unlock()
			return nil
		}}
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []string{"/help"}, e.sent(), "no Enter into the trust prompt")
	DropHeld(name, "test cleanup")
}
