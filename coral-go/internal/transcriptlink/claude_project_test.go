package transcriptlink

import (
	"strings"
	"testing"
)

// The expected names come from the Claude CLI's own functions (sanitize with /[^a-zA-Z0-9]/g, the 200-character cut, the 31-multiplier hash of the original path's UTF-16
// units, abs, base 36), run in node on each input (#5308). A scratch differential over 3,018 paths (ASCII, accents, CJK, emoji, backslashes, 0 to 300 characters, 1,172 of
// them hashed) had no disagreement.
func TestClaudeProjectDirName(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct{ name, path, want string }{
		{"a plain path", "/Users/dev/repo", "-Users-dev-repo"},
		{"a dot", "/Users/dev/my.project", "-Users-dev-my-project"},
		{"an underscore and a space", "/tmp/a_b c/d-e", "-tmp-a-b-c-d-e"},
		{"a Windows path", `C:\Users\dev\repo`, "C--Users-dev-repo"},
		{"a letter outside ASCII", "/Users/dev/café", "-Users-dev-caf-"},
		{"CJK, one dash per character", "/srv/日本語/プロジェクト", "-srv-----------"},
		{"an emoji is two UTF-16 units: two dashes", "/tmp/emoji-😀-dir", "-tmp-emoji----dir"},
		{"doubled and dotted separators", "/a//b/./c", "-a--b---c"},
		{"the root", "/", "-"},
		{"empty", "", ""},
		{"relative", "relative/path", "relative-path"},
		{"199 characters after the slash: 200 long, kept whole", "/" + a(199), "-" + a(199)},
		{"200 characters after the slash: 201 long, shortened", "/" + a(200), "-" + a(199) + "-" + "b6ymvl"},
		{"a hash that is positive", "/" + a(201), "-" + a(199) + "-" + "85qkr6"},
		{"a hash that is negative (abs)", "/" + strings.Repeat("b", 205), "-" + strings.Repeat("b", 199) + "-" + "npwu0d"},
		{"300 characters of directories", "/" + strings.Repeat("x/", 150), "-" + strings.Repeat("x-", 99) + "x" + "-" + "bdryjt"},
		{"dots in a long path", "/home/u/" + strings.Repeat("dir.with.dots/", 20), "-home-u-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-dots-dir-with-d-p2u79v"},
		{"250 accented letters (one unit each)", "/" + strings.Repeat("é", 250), strings.Repeat("-", 200) + "-" + "5hvcwf"},
		{"110 emoji (220 units, 221 characters)", "/" + strings.Repeat("😀", 110), strings.Repeat("-", 200) + "-" + "kaqd5"},
	}
	for _, c := range cases {
		if got := ClaudeProjectDirName(c.path); got != c.want {
			t.Errorf("%s: ClaudeProjectDirName(%q) = %q, want %q", c.name, trunc(c.path), trunc(got), trunc(c.want))
		}
	}
}

func trunc(s string) string {
	if len(s) > 60 {
		return s[:30] + "..." + s[len(s)-26:]
	}
	return s
}

// Only a name over 200 characters is shortened, and a shortened name is always 200 characters, a dash and a base-36 number.
func TestClaudeProjectDirNameLengths(t *testing.T) {
	for _, n := range []int{0, 1, 100, 199, 200} { // "/" plus n characters: n+1 long at most 201
		got := ClaudeProjectDirName("/" + strings.Repeat("q", n))
		if n+1 <= 200 && len(got) != n+1 {
			t.Errorf("a %d-character name was changed in length: %d", n+1, len(got))
		}
	}
	for _, n := range []int{200, 201, 500, 5000} {
		got := ClaudeProjectDirName("/" + strings.Repeat("q", n))
		head, hash, ok := strings.Cut(got[200:], "-")
		if len(got) <= 201 || head != "" || !ok || hash == "" || strings.Trim(hash, "0123456789abcdefghijklmnopqrstuvwxyz") != "" {
			t.Errorf("a %d-character path gave %q", n+1, trunc(got))
		}
	}
}
