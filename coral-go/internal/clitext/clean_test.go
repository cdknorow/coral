package clitext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClean(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain text, tab and newline stay", "line one\n\tline two", "line one\n\tline two"},
		{"unicode stays", "café — 日本語 ✓ 🙂", "café — 日本語 ✓ 🙂"},
		{"CSI colour", "\x1b[1;31mred\x1b[0m done", "red done"},
		{"CSI cursor movement and erase", "fake\x1b[2K\x1b[1A\x1b[10;5Hok", "fakeok"},
		{"private-mode CSI (hide the cursor, alt screen)", "a\x1b[?25l\x1b[?1049hb", "ab"},
		{"OSC title ended by BEL", "x\x1b]0;pwned title\x07y", "xy"},
		{"OSC title ended by ST", "x\x1b]2;pwned\x1b\\y", "xy"},
		{"OSC 52 clipboard write", "x\x1b]52;c;ZWNobyBwd25lZA==\x07y", "xy"},
		{"OSC 8 link keeps its visible text", "\x1b]8;;https://evil.test\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"APC", "a\x1b_payload\x1b\\b", "ab"},
		{"PM and SOS", "a\x1b^pm\x1b\\b\x1bXsos\x1b\\c", "abc"},
		{"two-byte escapes", "a\x1b7b\x1bcc\x1bMd", "abcd"},
		{"charset designation", "a\x1b(Bb", "ab"},
		{"bare ESC (before a newline, before a non-ASCII letter, at the end)", "a\x1b\nb\x1bé\x1b", "a\nbé"},
		{"C1 CSI (UTF-8) with its parameters", "a\u009b31mb", "ab"},
		{"C1 OSC (UTF-8) ended by C1 ST", "a\u009d0;title\u009cb", "ab"},
		{"other C1 controls", "a\u0085b\u008dc\u0090", "abc"},
		{"raw 8-bit C1 byte", "a\x9b31mb", "a31mb"},
		{"invalid UTF-8 that isn't C1 stays", "a\xffb", "a\xffb"},
		{"NUL, BEL, backspace, CR, form feed, DEL", "a\x00b\x07c\x08d\re\x0cf\x7fg", "abcdefg"},
		{"unterminated OSC loses only its introducer", "a\x1b]0;rest of the text", "a0;rest of the text"},
		{"unterminated CSI loses only its introducer", "a\x1b[31", "a31"},
		{"empty", "", ""},
		{"bidi override and isolates (Trojan Source)", "if (\u202eadmin\u202c) {", "if (admin) {"},
		{"LRM, RLM, ALM marks", "a\u200eb\u200fc\u061cd", "abcd"},
		{"line and paragraph separators and BOM", "a\u2028b\u2029c\ufeffd", "abcd"},
		{"zero-width joiner and non-joiner stay (emoji, scripts)", "\U0001f468\u200d\U0001f469 a\u200cb", "\U0001f468\u200d\U0001f469 a\u200cb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Clean(c.in)
			assert.Equal(t, c.want, got)
			assert.Equal(t, got, Clean(got), "cleaning twice changes nothing")
			for _, r := range got {
				if r == '\n' || r == '\t' {
					continue
				}
				assert.False(t, r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f), "a control is left: %q", got)
			}
		})
	}
}

func TestCleanJSON(t *testing.T) {
	t.Run("every string, keys too; numbers and structure kept", func(t *testing.T) {
		in := []byte(`{"messages":[{"id":12345678901234567,"content":"hi \u001b]52;c;cHduZWQ=\u0007there","subscriber_id":"Lead\u001b[2K"}],"n":1.50,"ok":true,"x\u001b[0m":null,"html":"<a&b>"}`)
		out := CleanJSON(in)
		assert.NotContains(t, string(out), `\u001b`)
		var v map[string]any
		d := json.NewDecoder(strings.NewReader(string(out)))
		d.UseNumber()
		require.NoError(t, d.Decode(&v))
		msg := v["messages"].([]any)[0].(map[string]any)
		assert.Equal(t, "hi there", msg["content"])
		assert.Equal(t, "Lead", msg["subscriber_id"])
		assert.Equal(t, json.Number("12345678901234567"), msg["id"], "big ids are not rounded")
		assert.Equal(t, json.Number("1.50"), v["n"])
		assert.Contains(t, v, "x")
		assert.Equal(t, "<a&b>", v["html"], "HTML characters aren't escaped")
	})
	t.Run("a clean reply is returned byte for byte", func(t *testing.T) {
		in := []byte("{\n  \"b\": 1,\n  \"a\": \"text\\twith tab\"\n}\n")
		assert.Equal(t, string(in), string(CleanJSON(in)))
	})
	t.Run("text that isn't JSON is cleaned as text", func(t *testing.T) {
		assert.Equal(t, "Error: no such board\n", string(CleanJSON([]byte("Error: \x1b[31mno such board\x1b[0m\n"))))
		assert.Equal(t, "{not json", string(CleanJSON([]byte("{not json\x1b[2J"))))
	})
	t.Run("a JSON string reply", func(t *testing.T) {
		assert.Equal(t, `"ab"`, string(CleanJSON([]byte(`"a\u001b[2Jb"`))))
	})
}

func TestTakeRaw(t *testing.T) {
	args, raw := TakeRaw([]string{"coral-board", "read", "--raw", "--last", "5"})
	assert.True(t, raw)
	assert.Equal(t, []string{"coral-board", "read", "--last", "5"}, args)
	args, raw = TakeRaw([]string{"coral-board", "post", "--", "--raw"})
	assert.False(t, raw, "after --, it is the message")
	assert.Equal(t, []string{"coral-board", "post", "--", "--raw"}, args)
	args, raw = TakeRaw([]string{"coral-board", "read"})
	assert.False(t, raw)
	assert.Equal(t, []string{"coral-board", "read"}, args)
}
