package clitext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3648: the shared cleaner drops the invisible characters that mean nothing in text, and keeps
// the ones real text needs.
func TestCleanDropsMeaninglessInvisibles(t *testing.T) {
	for name, r := range map[string]rune{
		"zero-width space":          0x200b,
		"word joiner":               0x2060,
		"function application":      0x2061,
		"invisible times":           0x2062,
		"invisible separator":       0x2063,
		"invisible plus":            0x2064,
		"Mongolian vowel separator": 0x180e,
		"tag begin":                 0xe0001,
		"tag space":                 0xe0020,
		"tag latin small a":         0xe0061,
		"cancel tag":                0xe007f,
	} {
		in := "Dev" + string(r) + "One"
		assert.Equal(t, "DevOne", Clean(in), name)
	}
	// A message spelled in tag characters disappears whole.
	hidden := string([]rune{0xe0069, 0xe0067, 0xe006e, 0xe006f, 0xe0072, 0xe0065})
	assert.Equal(t, "fine", Clean("fine"+hidden))
}

func TestCleanKeepsTextThatNeedsJoiners(t *testing.T) {
	for name, s := range map[string]string{
		"emoji family (ZWJ)":     "family: \U0001F468‍\U0001F469‍\U0001F467",
		"emoji profession (ZWJ)": "\U0001F469‍\U0001F4BB on it",
		"Persian word (ZWNJ)":    "می‌خواهم",
		"flag (regional pair)":   "\U0001F1EB\U0001F1F7",
		"soft hyphen in a word":  "hyphen­ation",
		"multi-line post":        "line one\n\tline two",
	} {
		assert.Equal(t, s, Clean(s), name)
	}
}

// Names and titles go further: no format character at all (joiners, soft hyphen), one line.
func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Dev‍One":              "DevOne",
		"Dev‌One":              "DevOne",
		"Dev­One":              "DevOne",
		"Dev​One\U000E0061":    "DevOne",
		"Dev‮One":              "DevOne",
		"  Dev\n\tOne \x1b[31mx  ": "Dev One x",
		"Plain Name":                "Plain Name",
	} {
		assert.Equal(t, want, CleanName(in), "%q", in)
	}
}
