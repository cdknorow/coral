package routes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// Security's #3723 finding: trim=N is chosen by the caller, a View-tier Coral Hub user among
// them, so no N may leave a piece of a token that the Hub's redactor then passes. Each token
// shape sits across the cut, for every N that crosses it (and at the 64 KB ceiling); the page
// is encoded as the handler sends it and run through the Hub's redactor: no 4 characters of
// the secret show, whatever N.

func trimSecretShown(out, secret string) string {
	for n := len(secret); n >= 4; n-- {
		for i := 0; i+n <= len(secret); i++ {
			if strings.Contains(out, secret[i:i+n]) {
				return secret[i : i+n]
			}
		}
	}
	return ""
}

// trimFake builds a fake token at run time (prefix + filler), as pkg/tunnel's tests do.
func trimFake(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

func TestTrimNeverShowsAPieceOfAToken(t *testing.T) {
	const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	hex := "0123456789abcdef"
	type token struct{ text, secret string }
	tokens := []token{
		{trimFake("ghp_", 36, alnum), trimFake("ghp_", 36, alnum)[4:]},
		{trimFake("github_pat_", 30, alnum+"_"), trimFake("github_pat_", 30, alnum+"_")[11:]},
		{trimFake("sk-ant-api03-", 40, alnum+"-_"), trimFake("sk-ant-api03-", 40, alnum+"-_")[13:]},
		{trimFake("AKIA", 16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"), trimFake("AKIA", 16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")[4:]},
		{trimFake("xoxb-", 30, alnum+"-"), trimFake("xoxb-", 30, alnum+"-")[5:]},
		{"Authorization: Bearer " + trimFake("", 40, alnum+"._-"), trimFake("", 40, alnum+"._-")},
		{`"authorization": "Basic ` + trimFake("", 24, alnum+"=") + `"`, trimFake("", 24, alnum+"=")},
		{`Authorization: Digest username="bob", response="` + trimFake("", 32, hex) + `"`, trimFake("", 32, hex)},
	}
	page := func(content string) []map[string]any {
		return []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": content}}
	}
	check := func(t *testing.T, content, secret string, n int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"messages": trimChatPage(page(content), n)})
		require.NoError(t, err)
		out := string(tunnel.RedactTokens(body))
		if shown := trimSecretShown(out, secret); shown != "" {
			t.Errorf("trim=%d shows %q of %q", n, shown, secret)
		}
	}
	for _, tk := range tokens {
		filler := strings.Repeat("log line\n", 28) // 252 bytes: the token crosses trim=256..
		content := filler + tk.text + "\nafter the token\n" + strings.Repeat("more output\n", 40)
		for n := trimMin; n <= len(filler)+len(tk.text)+2; n++ {
			check(t, content, tk.secret, n)
		}
		// At the ceiling: the token crosses 64 KB.
		big := strings.Repeat("x", trimMax-10) + " " + tk.text + " " + strings.Repeat("y", 100)
		for n := trimMax - 12; n <= trimMax; n++ {
			check(t, big, tk.secret, n)
		}
	}
}

// A PEM key cut anywhere by trim shows none of its body.
func TestTrimNeverShowsAPieceOfAPEMKey(t *testing.T) {
	var key strings.Builder
	key.WriteString("-----BEGIN RSA PRIVATE KEY-----\n")
	line := trimFake("", 64, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/")
	for i := 0; i < 8; i++ {
		key.WriteString(line + "\n")
	}
	key.WriteString("-----END RSA PRIVATE KEY-----")
	content := strings.Repeat("cat id_rsa\n", 20) + key.String() + "\ndone\n" + strings.Repeat("z", 1000)
	for n := trimMin; n <= len(content); n++ {
		body, _ := json.Marshal(map[string]any{"messages": trimChatPage([]map[string]any{{"type": "tool_result", "content": content}}, n)})
		out := string(tunnel.RedactTokens(body))
		if shown := trimSecretShown(out, line); shown != "" {
			t.Fatalf("trim=%d shows %q", n, shown)
		}
	}
}

// What is cut was redacted first: a token wholly inside the kept part is the marker there.
func TestTrimRedactsBeforeCutting(t *testing.T) {
	tok := trimFake("ghp_", 36, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	content := "token " + tok + "\n" + strings.Repeat("output\n", 100)
	got := trimChatPage([]map[string]any{{"type": "tool_result", "content": content}}, 300)[0]["content"].(string)
	assert.Contains(t, got, tunnel.RedactMarker)
	assert.NotContains(t, got, tok[4:12])
	assert.LessOrEqual(t, len(got), 300)
}
