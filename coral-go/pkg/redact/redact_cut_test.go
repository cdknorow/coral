package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every character a redactor rule accepts in a value, and a PEM key's body, is a token byte: a
// cut is never placed between two of them.
func TestTokenByteCoversEveryRule(t *testing.T) {
	for _, ru := range tokenRules {
		for c := 0; c < 256; c++ {
			if ru.class(byte(c)) {
				assert.True(t, tokenByte(byte(c)), "%q (rule %v) is a token byte", rune(c), ru.prefixes)
			}
		}
		for _, pre := range ru.prefixes {
			for i := 0; i < len(pre); i++ {
				// A cut at a space, ':' or '/' of a prefix ("Authorization: ", "://") leaves no
				// part of the value: what follows the cut is dropped whole.
				if !strings.ContainsRune(" :/", rune(pre[i])) {
					assert.True(t, tokenByte(pre[i]), "prefix %q byte %q", pre, pre[i])
				}
			}
		}
	}
	for _, c := range []byte(alnum + "+/=") {
		assert.True(t, tokenByte(c), "PEM body %q", c)
	}
}

// leaks reports the longest run of the secret (4 bytes or more) found in out, or "".
func leaks(out, secret string) string {
	for n := len(secret); n >= 4; n-- {
		for i := 0; i+n <= len(secret); i++ {
			if strings.Contains(out, secret[i:i+n]) {
				return secret[i : i+n]
			}
		}
	}
	return ""
}

// redactorTokens is each token shape the redactor recognises, with the part that must never
// show: the value (a keep-rule's keyword stays).
func redactorTokens() []struct{ text, secret string } {
	tok := func(text, secret string) struct{ text, secret string } {
		return struct{ text, secret string }{text, secret}
	}
	bearer := fake("", 40, alnum+"._-")
	basic := fake("", 24, alnum+"=")
	auth := fake("", 30, alnum)
	out := []struct{ text, secret string }{
		tok(fake("ghp_", 36, alnum), fake("ghp_", 36, alnum)[4:]),
		tok(fake("gho_", 40, alnum), fake("gho_", 40, alnum)[4:]),
		tok(fake("github_pat_", 30, alnum+"_"), fake("github_pat_", 30, alnum+"_")[11:]),
		tok(fake("sk-ant-api03-", 40, alnum+"-_"), fake("sk-ant-api03-", 40, alnum+"-_")[13:]),
		tok(fake("sk-proj-", 30, alnum), fake("sk-proj-", 30, alnum)[8:]),
		tok(fake("AKIA", 16, upper), fake("AKIA", 16, upper)[4:]),
		tok(fake("ASIA", 16, upper), fake("ASIA", 16, upper)[4:]),
		tok(fake("xoxb-", 30, alnum+"-"), fake("xoxb-", 30, alnum+"-")[5:]),
		tok("Authorization: Bearer "+bearer, bearer),
		tok(`"authorization": "Basic `+basic+`"`, basic),
		tok("-H 'Authorization: token "+auth+"'", auth),
		tok("bearer "+fake("", 25, alnum), fake("", 25, alnum)),
		tok(`Authorization: Digest username="bob", nonce="`+fake("", 32, "0123456789abcdef")+`"`, fake("", 32, "0123456789abcdef")),
		tok(fake("ghp_", 400, alnum), fake("ghp_", 400, alnum)[4:]),
		tok("postgres://app:"+fake("", 20, alnum)+"@db/main", fake("", 20, alnum)),
		tok("redis://:p@ss"+fake("", 16, alnum)+"@cache:6379", "p@ss"+fake("", 16, alnum)),
	}
	return out
}

// A token cut anywhere, then sent as it would be (raw, and as a JSON string through the Hub's
// redactor): at most 3 characters of its secret show, at every cut offset.
func TestCutOutsideTokenNeverLeavesAPieceOfAToken(t *testing.T) {
	for _, tk := range redactorTokens() {
		for _, s := range []string{"see " + tk.text + " done", tk.text, "é" + tk.text + "\nnext"} {
			for n := 0; n <= len(s)+1; n++ {
				p := Cut(s, n)
				require.LessOrEqual(t, p, max(n, 0))
				cut := s[:p]
				assert.True(t, utf8.ValidString(cut), "a cut at a character boundary")
				raw := string(Tokens([]byte(cut)))
				assert.Empty(t, leaks(raw, tk.secret), "%q cut at %d (raw): %q", tk.text[:min(12, len(tk.text))], n, raw)
				body, _ := json.Marshal(map[string]string{"content": cut})
				red := string(Tokens(body))
				assert.Empty(t, leaks(red, tk.secret), "%q cut at %d (JSON): %q", tk.text[:min(12, len(tk.text))], n, red)
			}
		}
	}
}

// A PEM key cut anywhere, as a JSON string through the redactor: none of its body shows.
func TestCutOutsideTokenPEMCutAnywhere(t *testing.T) {
	key := pemKey("RSA ", 6, "\n")
	body := strings.Split(key, "\n")[1]
	s := "cat id_rsa\n" + key + "\ndone"
	for n := 0; n <= len(s); n++ {
		cut := s[:Cut(s, n)]
		js, _ := json.Marshal(map[string]string{"content": cut})
		red := string(Tokens(js))
		assert.Empty(t, leaks(red, body), "cut at %d: %q", n, red)
	}
}

func TestCutOutsideTokenPlaces(t *testing.T) {
	assert.Equal(t, 3, Cut("ab ghp_xyz", 6), "back to before the run")
	assert.Equal(t, 0, Cut("abcdef", 3), "a run from the start: nothing")
	assert.Equal(t, 6, Cut("abc de", 9), "past the end: all")
	assert.Equal(t, 4, Cut("abc def", 4), "right after a space")
	assert.Equal(t, 0, Cut("éé", 1), "never inside a character")
}
