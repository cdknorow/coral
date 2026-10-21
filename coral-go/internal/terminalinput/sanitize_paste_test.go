package terminalinput

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizePasteKeepsTextButRemovesControlInput(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb\n\tc":                    "a\nb\n\tc",
		"crlf\r\nline\rmac":            "crlf\nline\nmac",
		"esc\x1b[201~inject":           "esc[201~inject",
		"nul\x00bell\acontrol\x7f":     "nulbellcontrol",
		"unicode — ✓ é":                "unicode — ✓ é",
		"c1\u0085\u009bstripped":       "c1stripped",
		"[Coral UI panel request x] y": "[Coral UI panel request x] y",
	} {
		require.Equal(t, want, SanitizePaste(in), "%q", in)
	}
}
