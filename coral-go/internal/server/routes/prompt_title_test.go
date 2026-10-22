package routes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #306 (QA's #278 row 15): a dialog's heading is told from a wrapped question, so the card can show it on its own line. Question
// stays the whole text (auto-accept, samePrompt and the session state read it as before).
func TestPromptScreenTitle(t *testing.T) {
	menu := "› 1. Yes\n  2. No\n  Press enter to confirm or esc to go back\n"
	for _, c := range []struct {
		name, screen, title, question string
	}{
		{"a heading over its text", "  Hooks need review\n  2 hooks are new or changed.\n  Hooks can run outside the sandbox after you trust them.\n" + menu,
			"Hooks need review", "Hooks need review 2 hooks are new or changed. Hooks can run outside the sandbox after you trust them."},
		{"a wrapped question: its top line is full", "  Do you want to allow Claude to make this edit to the\n  file?\n" + menu,
			"", "Do you want to allow Claude to make this edit to the file?"},
		{"a top line that ends a sentence", "  Update ready.\n  Restart now to use the new version?\n" + menu,
			"", "Update ready. Restart now to use the new version?"},
		{"one line", "  Do you want to proceed?\n" + menu, "", "Do you want to proceed?"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, ok := parsePromptScreen(c.screen)
			require.True(t, ok, c.screen)
			assert.Equal(t, c.title, s.Title)
			assert.Equal(t, c.question, s.Question)
		})
	}
}

func TestWrapsInto(t *testing.T) {
	assert.True(t, wrapsInto("Do you want to allow Claude to make this edit to the", "file?"), "the next word did not fit")
	assert.False(t, wrapsInto("Hooks need review", "2 hooks are new or changed."), "the next word would have fitted")
	assert.True(t, wrapsInto(strings.Repeat("x", 30), "abc def"), "a long line above a short one")
}

// Security on #132: what can hide or reorder a prompt's text is dropped from the title, the question and the labels (the card
// shows them, auto-accept matches the question); escapes are written as escapes here, never as the characters.
func TestPromptScreenTextIsClean(t *testing.T) {
	screen := "  Hooks need \u202ereview\n  2 hooks are new\u200b or changed.\n" +
		"\u203a 1. Re\u00adview hooks\U000E0041\n  2. Trust \x1b[31mall\x1b[0m and continue\n  Press enter to confirm or esc to go back\n"
	s, ok := parsePromptScreen(screen)
	require.True(t, ok)
	assert.Equal(t, "Hooks need review", s.Title)
	assert.Equal(t, "Hooks need review 2 hooks are new or changed.", s.Question)
	assert.Equal(t, []string{"Review hooks", "Trust all and continue"}, []string{s.Options[0].Label, s.Options[1].Label})
	for _, text := range []string{s.Title, s.Question, s.Options[0].Label, s.Options[1].Label} {
		for _, r := range text {
			assert.False(t, r < 0x20 || r == 0x7f || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || r == 0xad || r >= 0xe0000, "%q keeps %U", text, r)
		}
	}
}

// Every screen in testdata reads the same with and without the cleaning (none carries such characters), so auto-accept,
// samePrompt and the session state see what they saw before.
func TestPromptScreenCleaningLeavesTestdataAlone(t *testing.T) {
	files, err := filepath.Glob("testdata/*/*")
	require.NoError(t, err)
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, ".raw") {
			continue // a pty's bytes, rendered to a screen before it is parsed (the screens are the .txt files)
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		raw, ok := parsePromptScreenRaw(string(b))
		if !ok {
			continue
		}
		n++
		clean, _ := parsePromptScreen(string(b))
		assert.Equal(t, raw.Question, clean.Question, f)
		assert.Equal(t, raw.Title, clean.Title, f)
		require.Equal(t, len(raw.Options), len(clean.Options), f)
		for i := range raw.Options {
			assert.Equal(t, raw.Options[i].Label, clean.Options[i].Label, f)
		}
	}
	assert.Greater(t, n, 5, "the testdata screens were read")
}
