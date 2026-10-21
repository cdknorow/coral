package routes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/pkg/redact"
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
	t.Parallel() // no shared state: these ran one after another (#5622)
	const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	type token struct{ text, secret string }
	// Shapes betterleaks' rules find whole (#3808: they are Coral's rules now); the ones only
	// Coral's old rules found are listed in the #3808 gap report.
	tokens := []token{
		{trimFake("ghp_", 36, alnum), trimFake("ghp_", 36, alnum)[4:]},
		{trimFake("github_pat_", 82, alnum+"_"), trimFake("github_pat_", 82, alnum+"_")[11:]},
		{trimFake("sk-ant-api03-", 93, alnum+"-_") + "AA", trimFake("sk-ant-api03-", 93, alnum+"-_")[13:]},
		{"xoxb-" + trimFake("", 11, "0123456789") + "-" + trimFake("", 12, "0123456789") + "-" + trimFake("", 24, alnum), trimFake("", 24, alnum)},
		{"sk_" + trimFake("live_", 24, alnum), trimFake("live_", 24, alnum)[5:]},
	}
	page := func(content string) []map[string]any {
		return []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": content}}
	}
	// check is called from several goroutines (#4852): it reports with t.Errorf, never t.Fatal or require.
	check := func(t *testing.T, content, secret string, n int) {
		body, err := json.Marshal(map[string]any{"messages": trimChatPage(page(content), n, secrets.Redactor())})
		if err != nil {
			t.Errorf("trim=%d: %v", n, err)
			return
		}
		red, err := secrets.Redactor().Reply(body, true)
		if err != nil {
			t.Errorf("trim=%d: %v", n, err)
			return
		}
		out := string(red)
		if shown := trimSecretShown(out, secret); shown != "" {
			t.Errorf("trim=%d shows %q of %q", n, shown, secret)
		}
	}
	for _, tk := range tokens {
		filler := strings.Repeat("log line\n", 28) // 252 bytes: the token crosses trim=256..
		content := filler + tk.text + "\nafter the token\n" + strings.Repeat("more output\n", 40)
		lo, hi := trimMin, len(filler)+len(tk.text)+2
		parallelFor(hi-lo+1, func(i int) bool { check(t, content, tk.secret, lo+i); return true })
		// At the ceiling: the token crosses 64 KB.
		big := strings.Repeat("x", trimMax-10) + " " + tk.text + " " + strings.Repeat("y", 100)
		parallelFor(13, func(i int) bool { check(t, big, tk.secret, trimMax-12+i); return true })
	}
}

// A PEM key cut anywhere by trim shows none of its body.
func TestTrimNeverShowsAPieceOfAPEMKey(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	var key strings.Builder
	key.WriteString("-----BEGIN RSA PRIVATE KEY-----\n")
	line := trimFake("", 64, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/")
	for i := 0; i < 8; i++ {
		key.WriteString(line + "\n")
	}
	key.WriteString("-----END RSA PRIVATE KEY-----")
	content := strings.Repeat("cat id_rsa\n", 20) + key.String() + "\ndone\n" + strings.Repeat("z", 1000)
	parallelFor(len(content)-trimMin+1, func(i int) bool { // on several goroutines: t.Errorf, not t.Fatal (#4852)
		n := trimMin + i
		body, _ := json.Marshal(map[string]any{"messages": trimChatPage([]map[string]any{{"type": "tool_result", "content": content}}, n, secrets.Redactor())})
		red, err := secrets.Redactor().Reply(body, true)
		if err != nil {
			t.Errorf("trim=%d: %v", n, err)
			return false
		}
		if shown := trimSecretShown(string(red), line); shown != "" {
			t.Errorf("trim=%d shows %q", n, shown)
			return false
		}
		return true
	})
}

// What is cut was redacted first: a token wholly inside the kept part is the marker there.
func TestTrimRedactsBeforeCutting(t *testing.T) {
	tok := trimFake("ghp_", 36, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	content := "token " + tok + "\n" + strings.Repeat("output\n", 100)
	got := trimChatPage([]map[string]any{{"type": "tool_result", "content": content}}, 300, secrets.Redactor())[0]["content"].(string)
	assert.Contains(t, got, redact.Marker)
	assert.NotContains(t, got, tok[4:12])
	assert.LessOrEqual(t, len(got), 300)
}
