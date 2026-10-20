package board

import (
	"strings"
	"testing"
)

func TestTextHasFoldMatchesToLower(t *testing.T) {
	texts := []string{"", "Hello World", "HELLO", "the quick Brown fox", "KELVIN \u212a sign", "\u0130stanbul", "\u01c5 title", "x", "aaab", "tab\tnew\nline", "na\u00efve Caf\u00e9", "\u212a", "ab\u212aab"}
	needles := []string{"", "h", "hello", "world", "brown", "k", "kelvin", "i\u0307", "\u00ef", "\u00e9", "aab", "ab", "\t", "zz", "fox ", "kab", "abk"}
	for _, s := range texts {
		for _, n := range needles {
			n = strings.ToLower(n)
			if got, want := textHasFold(s, n), strings.Contains(strings.ToLower(s), n); got != want {
				t.Errorf("textHasFold(%q, %q) = %v, ToLower says %v", s, n, got, want)
			}
		}
	}
}

func BenchmarkTextHasFold(b *testing.B) {
	s := strings.Repeat("Do the Review work for item 4711, then post the result. ", 4)
	b.Run("fold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			textHasFold(s, "zzzz")
		}
	})
	b.Run("tolower", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			strings.Contains(strings.ToLower(s), "zzzz")
		}
	})
}
