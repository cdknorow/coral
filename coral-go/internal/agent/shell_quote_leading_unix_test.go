//go:build !windows

package agent

import (
	"os/exec"
	"testing"
)

// #3930 (Security's #3927 finding 3): a word starting with "=", "%" or "@" is quoted, so zsh's
// EQUALS expansion (=curl becomes /usr/bin/curl) can't rewrite it; the same bytes inside a word
// stay plain. Each word, once quoted, comes back unchanged from a real zsh and sh.
func TestShellQuoteLeadingSpecials(t *testing.T) {
	for in, want := range map[string]string{
		"=curl":         "'=curl'",
		"%1":            "'%1'",
		"@file":         "'@file'",
		"--flag=value":  "--flag=value",
		"user@host":     "user@host",
		"50%":           "50%",
		"a=b":           "a=b",
		"=":             "'='",
		"=ls'; touch x": `'=ls'\''; touch x'`,
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
	for _, sh := range []string{"zsh", "sh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		for _, in := range []string{"=curl", "=ls", "%1", "@file", "=ls'; touch x"} {
			out, err := exec.Command(path, "-c", "printf '%s' "+shellQuote(in)).Output()
			if err != nil {
				t.Fatalf("%s: %v", sh, err)
			}
			if string(out) != in {
				t.Errorf("%s turned %q into %q", sh, in, out)
			}
		}
	}
}
