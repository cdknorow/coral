package routes

import (
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
