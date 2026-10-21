package secrets

import (
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/pkg/redact"
)

// #5158: keywordSpans looks only where a name could be; it must find exactly what the rules find over the whole text. The test compares the bytes
// each marks, on the shapes, on the text the repository carries, on long lines and on random text built from the pieces the rules read.

func covered(n int, spans []redact.Span) []bool {
	b := make([]bool, n)
	for _, sp := range spans {
		for i := sp.Start; i < sp.End; i++ {
			b[i] = true
		}
	}
	return b
}

func sameCoverage(t *testing.T, what, text string) {
	t.Helper()
	got, want := covered(len(text), keywordSpans(text)), covered(len(text), keywordSpansIn(text))
	for i := range want {
		if got[i] != want[i] {
			lo, hi := max(0, i-60), min(len(text), i+60)
			t.Fatalf("%s: byte %d is %v gated, %v over the whole text; around it: %q", what, i, got[i], want[i], text[lo:hi])
		}
	}
}

func TestKeywordGateFindsWhatTheRulesFind(t *testing.T) {
	for _, sh := range append(shapes(), shapes2()...) {
		r := rand.New(rand.NewSource(58))
		for i := 0; i < 200; i++ {
			text := sh.text(sh.val.make(r))
			sameCoverage(t, sh.name, text)
			sameCoverage(t, sh.name+" in a line", "x = 1\n"+text+"\ny: 2\n")
		}
	}
	var files []string
	for _, g := range []string{"../../agent_docs/*.md", "../../site_docs/*.md", "*.go", "../../pkg/redact/*.go"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	require.NotEmpty(t, files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		sameCoverage(t, f, string(b))
	}
}

// Lines longer than the look-back and values longer than the window.
func TestKeywordGateOnLongLines(t *testing.T) {
	long := strings.Repeat("ab ", 2000)
	val := strings.Repeat("Zq8Lm2Xv", 3000)
	for name, text := range map[string]string{
		"name after a long line":   long + "api_key=" + val[:40],
		"value past the window":    "password=" + val + " tail",
		"key value past window":    "token=" + val + "\nnext",
		"cookie past the window":   "Cookie: a=1; session=" + val + "; b=2",
		"name chain over lookback": strings.Repeat("a_", 800) + "token=" + val[:40],
		"flag, long value":         "tool --password " + val + " run",
		"encoded, long value":      "x%3Dy&api_key%3D" + strings.Repeat("A%2B", 4000) + "%26z",
		"curl long line":           "curl " + long + " -u bob:" + val[:30],
		"mysql long line":          "mysql " + long + " -p" + val[:30],
		"json escaped, long":       strings.Repeat("x", 3000) + `\"api_key\": \"` + val[:40] + `\"`,
	} {
		sameCoverage(t, name, text)
	}
}

func TestKeywordGateOnRandomText(t *testing.T) {
	pieces := []string{"api_key", "token", "TOKEN", "x-api-key", "password", "Password", "secret", "mytoken", "tokens", "DB_PASSWORD", "PGPASSWORD", "MYSQL_PWD",
		"AWS_SECRET", "STRIPE_KEY", "key", "sig", "auth", "authorization", "Authorization", "Cookie", "Set-Cookie", "session", "SID", "curl", "-u", "--user",
		"mysql", "-p", "sshpass", "--password", "--token", "--api-key", "--", "-", "bearer", "Bearer", "=", ":", "＝", "：", "%3D", "%253D", "%3F", "%3A", "%253A", "%20", "+", "&quot;", "&#34;", "&#x22;", "&apos;", "&#39;", "&amp;", "%26", "&", "?", ";", ",",
		" ", "  ", "\t", "\n", "\r\n", `"`, "'", `\"`, `\\\"`, "{", "}", "[", "]", "(", ")", "<", ">", "$", "%", "_", ".", "/", "+",
		"Zq8Lm2Xv9Rt4Wk7Pd3Hs", "\u017f", "\u212a", strings.Repeat("a-", 40), strings.Repeat(" ", 300), strings.Repeat("\n", 300), strings.Repeat("ab", 5000), "abcdefgh", "0123456789abcdef", "a1", "x", "AAAA", "https://h.example.com/cb", "next=", "user:pw", "\u200b", "é"}
	r := rand.New(rand.NewSource(5158))
	n := 4000
	if v, err := strconv.Atoi(os.Getenv("KEYWORD_GATE_ITERATIONS")); err == nil {
		n = v // a longer run on demand
	}
	for i := 0; i < n; i++ {
		var b strings.Builder
		for n := 5 + r.Intn(60); n > 0; n-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		sameCoverage(t, "random", b.String())
	}
}

// The gate's places are a name and its value, not the kilobytes after them (#5209): on ordinary text with a name here and there the rules read
// a few percent of the bytes. A window that grew back to a fixed size would cost the hub a third of its speed on source-code-heavy terminal text.
func TestKeywordPlacesAreTight(t *testing.T) {
	filler := strings.Repeat("an ordinary line of output, with words and numbers 12345\n", 60) // about 3.4 KB
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(filler)
		b.WriteString("export DEPLOY_TOKEN=Zq8Lm2Xv9Rt4Wk7Pd3Hs" + strings.Repeat("x", i%7) + "\n")
		b.WriteString(`curl -H "x-api-key: Zq8Lm2Xv9Rt4Wk7Pd3Hs" https://example.com/` + "\n")
	}
	text := b.String()
	in := 0
	for _, p := range keywordPlaces(text) {
		in += p.end - p.start
	}
	if in*20 > len(text) { // over 5 percent
		t.Fatalf("the places hold %d of %d bytes: the windows are too wide", in, len(text))
	}
	if got := len(keywordSpans(text)); got < 400 {
		t.Fatalf("%d values found, want 400", got)
	}
}
