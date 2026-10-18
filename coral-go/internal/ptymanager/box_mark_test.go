package ptymanager

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// A message in the input box by its mark (#4587): what TextInInputBox says for the message, without keeping it.
func TestBoxMarkInBox(t *testing.T) {
	codex := func(box string) string {
		return "• Working (2s)\n\n› " + box + "\n\n  gpt-6-astra medium · ~/cx\n"
	} // as Codex draws it: blank lines around the box
	long := strings.Repeat("deploy the staging build and report back ", 12) // ~490 chars, wraps
	for name, c := range map[string]struct {
		text, screen string
		want         bool
	}{
		"short, exactly":                  {"do", codex("do"), true},
		"short, the placeholder holds it": {"do", codex("Ask Codex to do anything"), false},
		"short, more text after it":       {"deploy", codex("deploy to staging"), false},
		"white space does not matter":     {"deploy  to\nstaging", codex("deploy to staging"), true},
		"empty box":                       {"deploy", codex(""), false},
		"long, its start in the box":      {long, codex(long[:120] + "\n  " + long[120:240]), true},
		"long, another start":             {long, codex("something else entirely, long enough to wrap"), false},
		"multi-line, a paste placeholder": {"line one\nline two", codex("[Pasted text #1 +2 lines]"), true},
		"one line, a paste placeholder":   {"line one", codex("[Pasted text #1 +2 lines]"), false},
		"no box":                          {"do", "just output\n", false},
	} {
		assert.Equal(t, c.want, MarkOf(c.text).InBox(c.screen), name)
		assert.Equal(t, c.want, TextInInputBox(c.screen, c.text), name+" (TextInInputBox)")
	}
}

func TestBoxMarkKeepsNoMoreThanItNeeds(t *testing.T) {
	secret := strings.Repeat("a", 300) + "SECRET-TAIL"
	m := MarkOf(secret)
	assert.LessOrEqual(t, utf8.RuneCountInString(m.prefix), boxMarkPrefixRunes)
	assert.NotContains(t, m.prefix, "SECRET-TAIL", "only the start is kept")
	assert.True(t, m.Of(secret))
	assert.False(t, m.Of(secret+" "), "another message, by hash and length")
	assert.False(t, m.Of(strings.Repeat("a", 300)+"SECRET-TAII"), "same length, another hash")
	assert.False(t, BoxMark{}.Of(""), "the zero mark is no message")
	assert.True(t, BoxMark{}.IsZero())
	assert.False(t, MarkOf("x").IsZero())
}
