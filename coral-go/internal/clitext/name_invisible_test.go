package clitext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3656: in a name, the invisible characters that aren't format characters go too: variation
// selectors, Hangul fillers and the braille blank, which make names look alike or empty.
func TestCleanNameDropsLookAlikeInvisibles(t *testing.T) {
	for name, r := range map[string]rune{
		"variation selector 1":          0xfe00,
		"variation selector 16 (emoji)": 0xfe0f,
		"variation selector 17":         0xe0100,
		"variation selector 256":        0xe01ef,
		"Hangul choseong filler":        0x115f,
		"Hangul jungseong filler":       0x1160,
		"Hangul filler":                 0x3164,
		"halfwidth Hangul filler":       0xffa0,
		"braille blank":                 0x2800,
	} {
		assert.Equal(t, "DevOne", CleanName("Dev"+string(r)+"One"), name)
	}
	// A name of nothing but fillers is empty, not a name that only looks empty.
	assert.Equal(t, "", CleanName("\u3164\u2800\u115f"))
	// Text keeps them: an emoji's colour form and the text around a filler.
	assert.Equal(t, "❤\ufe0f thanks", Clean("❤\ufe0f thanks"))
	assert.Equal(t, "a\u3164b", Clean("a\u3164b"))
	// Visible neighbours are untouched.
	for _, keep := range []string{"Dev One", "가나", "⠁dots"} {
		assert.Equal(t, keep, CleanName(keep))
	}
}
