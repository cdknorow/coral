package routes

import (
	"strings"
	"testing"
)

// Security's finding on #6207: the search excerpt is a window cut out of the transcript text, and its ends can fall inside a secret. Redacted after, the cut token is no longer a token (a GitHub token short of
// its last character passed unchanged). The text is redacted whole before the window is cut, so at either edge, at every offset, no run of the secret longer than 8 characters is shown.
func TestASecretAtTheEdgeOfTheSearchExcerptIsNotShownCut(t *testing.T) {
	secrets := map[string]string{
		"a GitHub token":    "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "kLmNoP",
		"an AWS access key": "AKIAJ7QX2LNB4MWT9RZD",
		"an Anthropic key":  "sk-ant-api03-" + strings.Repeat("Zx9Qw8Er7T", 4),
	}
	longestRun := func(secret, shown string) int {
		longest := 0
		for i := 0; i < len(secret); i++ {
			for j := i + longest + 1; j <= len(secret); j++ {
				if !strings.Contains(shown, secret[i:j]) {
					break
				}
				longest = j - i
			}
		}
		return longest
	}
	const window = 300
	filler := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n/5+1)[:n]) }
	for name, secret := range secrets {
		for off := 0; off <= len(secret); off++ {
			// the window's LEFT edge falls near the secret's start, or its RIGHT edge near its end, with the match "needle" in the middle
			for _, which := range []string{"left", "right"} {
				for shift := -3; shift <= 3; shift++ { // a few offsets around where the edge lands inside the secret
					var padded string
					if which == "left" {
						padded = filler(off+shift+170+400) + " " + secret + " " + filler(100) + " needle " + filler(400)
					} else {
						padded = filler(400) + " needle " + filler(150-off-shift+10) + " " + secret + " " + filler(400)
					}
					idx := strings.Index(padded, "needle")
					if got := makeBoundedExcerpt(padded, idx, window); longestRun(secret, got) > 8 {
						t.Fatalf("%s, %s edge, off %d shift %d: %d characters of the secret are shown: %q", name, which, off, shift, longestRun(secret, got), got)
					}
				}
			}
		}
	}
	if got := makeBoundedExcerpt(strings.Repeat("a b ", 200)+"needle"+strings.Repeat(" c d", 200), 800, 300); !strings.Contains(got, "needle") || len([]rune(got)) > 306 {
		t.Fatalf("an ordinary excerpt is unchanged: %q", got)
	}
}
