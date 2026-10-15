package tunnel

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fake tokens are built at run time (prefix + filler), so the repository holds nothing that
// looks like a real secret.
func fake(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

const (
	alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

func pemKey(label string, lines int, nl string) string {
	var b strings.Builder
	b.WriteString("-----BEGIN " + label + "PRIVATE KEY-----" + nl)
	for i := 0; i < lines; i++ {
		b.WriteString(fake("", 64, alnum+"+/") + nl)
	}
	b.WriteString("-----END " + label + "PRIVATE KEY-----")
	return b.String()
}

func TestRedactEachRule(t *testing.T) {
	m := RedactMarker
	for _, c := range []struct{ in, want string }{
		{"token " + fake("ghp_", 36, alnum) + " end", "token " + m + " end"},
		{fake("gho_", 40, alnum), m},
		{"x=" + fake("github_pat_", 30, alnum+"_"), "x=" + m},
		{"key: " + fake("sk-ant-api03-", 40, alnum+"-_") + ".", "key: " + m + "."},
		{fake("sk-proj-", 30, alnum), m},
		{"AWS " + fake("AKIA", 16, upper) + " ok", "AWS " + m + " ok"},
		{fake("ASIA", 16, upper), m},
		{"slack " + fake("xoxb-", 30, alnum+"-"), "slack " + m},
		{"Authorization: Bearer " + fake("", 40, alnum+"._-"), "Authorization: Bearer " + m},
		{`{"authorization": "Basic ` + fake("", 24, alnum+"=") + `"}`, `{"authorization": "Basic ` + m + `"}`},
		{"curl -H 'Authorization: token " + fake("", 30, alnum) + "'", "curl -H 'Authorization: token " + m + "'"},
		{"bearer " + fake("", 25, alnum), "bearer " + m},
	} {
		assert.Equal(t, c.want, string(RedactTokens([]byte(c.in))), c.in)
	}
}

func TestRedactNearMisses(t *testing.T) {
	for _, in := range []string{
		fake("ghp_", 30, alnum),                                        // too short
		"x" + fake("ghp_", 36, alnum),                                  // no boundary before it
		"sk-learn and task-runner and risk-free",                       // prose
		fake("sk-", 12, alnum),                                         // too short
		fake("AKIA", 15, upper) + " ",                                  // 15, not 16
		fake("AKIA", 17, upper),                                        // 17: not an access key id
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",                     // a git SHA
		"3fa85f64-5717-4562-b3fc-2c963f66afa6",                         // a UUID
		"sha256:" + strings.Repeat("ab", 32),                           // a digest
		"The Bearer token goes in the header",                          // Bearer in prose
		"Authorization header is missing",                              // Authorization in prose
		"Authorization: Bearer short",                                  // too short a value
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", // not a private key
		"-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----",
	} {
		assert.Equal(t, in, string(RedactTokens([]byte(in))), in)
	}
}

func TestRedactPEM(t *testing.T) {
	m := RedactMarker
	key := pemKey("RSA ", 20, "\n")
	assert.Equal(t, "before\n"+m+"\nafter", string(RedactTokens([]byte("before\n"+key+"\nafter"))))
	assert.Equal(t, m, string(RedactTokens([]byte(pemKey("", 3, "\n")))))
	assert.Equal(t, m, string(RedactTokens([]byte(pemKey("OPENSSH ", 3, "\r\n")))))

	// Cut off: everything after BEGIN is dropped, to the end of the reply.
	cut := key[:len(key)/2]
	assert.Equal(t, "x "+m, string(RedactTokens([]byte("x "+cut))))

	// Over 16 KiB, no END: still never flushed (QA's note on the design).
	big := pemKey("EC ", 600, "\n")
	big = big[:strings.LastIndex(big, "-----END")]
	require.Greater(t, len(big), 32<<10)
	assert.Equal(t, "a "+m, string(RedactTokens([]byte("a "+big))))

	// In JSON, with escaped newlines: the key goes, the JSON stays valid.
	body, _ := json.Marshal(map[string]string{"output": "cat id_rsa\n" + key + "\ndone", "next": "ok"})
	red := RedactTokens(body)
	var got map[string]string
	require.NoError(t, json.Unmarshal(red, &got), string(red))
	assert.Equal(t, "cat id_rsa\n"+m+"\ndone", got["output"])
	assert.Equal(t, "ok", got["next"])

	// Cut off inside a JSON string: the key ends at the closing quote.
	body, _ = json.Marshal(map[string]string{"output": cut, "next": "ok"})
	red = RedactTokens(body)
	require.NoError(t, json.Unmarshal(red, &got), string(red))
	assert.Equal(t, m, got["output"])
	assert.Equal(t, "ok", got["next"])
}

// Tokens inside JSON strings, next to escapes: the body still parses and the tokens are gone.
func TestRedactKeepsJSONValid(t *testing.T) {
	tokens := []string{fake("ghp_", 36, alnum), fake("sk-", 30, alnum), fake("AKIA", 16, upper), fake("xoxp-", 20, alnum)}
	for i, tok := range tokens {
		for _, s := range []string{tok, "\\" + tok, "\"" + tok + "\"", "line\n" + tok + "\tend", "<b>" + tok + "</b>", "export X=" + tok} {
			body, _ := json.Marshal(map[string]any{"text": s, "n": i, "list": []string{s, "plain"}})
			red := RedactTokens(body)
			var v map[string]any
			require.NoError(t, json.Unmarshal(red, &v), "%s -> %s", body, red)
			assert.NotContains(t, string(red), tok)
			assert.Contains(t, string(red), RedactMarker)
		}
	}
}

// The property the stream depends on: any split of a body gives the same bytes as one write.
func TestRedactStreamEqualsWhole(t *testing.T) {
	parts := []string{
		"plain text ", fake("ghp_", 40, alnum), " and ", fake("sk-ant-", 30, alnum+"-"), "\n",
		"Authorization: Bearer ", fake("", 60, alnum+"."), " ", fake("AKIA", 16, upper), " gh", "p_ short ",
		pemKey("RSA ", 4, "\n"), " tail ", fake("xoxb-", 15, alnum), " sk", "-",
		`{"k":"`, strings.ReplaceAll(pemKey("", 2, "\n"), "\n", `\n`), `","z":1}`,
		"-----BEGIN PUBLIC KEY-----x", " -----BEGIN ", "authorization=", fake("", 10, alnum),
	}
	body := []byte(strings.Join(parts, ""))
	whole := RedactTokens(body)
	assert.NotContains(t, string(whole), fake("ghp_", 40, alnum))
	assert.Contains(t, string(whole), "ghp_ short", "the near miss stays")
	for split := 0; split <= len(body); split++ {
		var out bytes.Buffer
		r := NewRedactor(&out)
		_, _ = r.Write(body[:split])
		_, _ = r.Write(body[split:])
		require.NoError(t, r.Close())
		require.Equal(t, string(whole), out.String(), "split at %d", split)
	}
	rng := rand.New(rand.NewSource(3560))
	for trial := 0; trial < 300; trial++ {
		var out bytes.Buffer
		r := NewRedactor(&out)
		for rest := body; len(rest) > 0; {
			n := 1 + rng.Intn(min(len(rest), 40))
			_, _ = r.Write(rest[:n])
			rest = rest[n:]
		}
		require.NoError(t, r.Close())
		require.Equal(t, string(whole), out.String(), "trial %d", trial)
	}
}

// A long PEM split over many chunks, and a long reply with no token: memory stays bounded
// (what's held back never grows past one rule's span).
func TestRedactStreamHoldsLittle(t *testing.T) {
	big := pemKey("EC ", 2000, "\n") // ~130 KiB
	var out bytes.Buffer
	r := NewRedactor(&out)
	in := []byte("x " + big + " y")
	for len(in) > 0 {
		n := min(len(in), 4096)
		_, _ = r.Write(in[:n])
		in = in[n:]
		assert.LessOrEqual(t, len(r.pending), 600)
	}
	require.NoError(t, r.Close())
	assert.Equal(t, "x "+RedactMarker+" y", out.String())

	out.Reset()
	r = NewRedactor(&out)
	chunk := []byte(strings.Repeat("ordinary log line without secrets 0123456789\n", 800))
	for i := 0; i < 50; i++ {
		_, _ = r.Write(chunk)
		assert.LessOrEqual(t, len(r.pending), 16)
	}
	require.NoError(t, r.Close())
	assert.Equal(t, 50*len(chunk), out.Len())
}

// Security's #3560 review (MUST 1): a token longer than its rule's ceiling is still redacted
// whole (one marker, no tail), in one write and split at every offset.
func TestRedactLongTokens(t *testing.T) {
	m := RedactMarker
	jwt := fake("eyJ", 500, alnum+"_-") + "." + fake("", 600, alnum+"_-") + "." + fake("", 396, alnum+"_-")
	require.Greater(t, len(jwt), 1400)
	longSK := fake("sk-", 300, alnum)
	longGH := fake("ghp_", 400, alnum)
	for _, c := range []struct{ in, want string }{
		{"Authorization: Bearer " + jwt + "\nnext", "Authorization: Bearer " + m + "\nnext"},
		{`{"h":"Bearer ` + jwt + `"}`, `{"h":"Bearer ` + m + `"}`},
		{"key=" + longSK + " ok", "key=" + m + " ok"},
		{longGH, m},
		{"AKIA" + fake("", 20, upper), "AKIA" + fake("", 20, upper)}, // an exact-length rule: 20 isn't an access key id
	} {
		assert.Equal(t, c.want, string(RedactTokens([]byte(c.in))), c.in[:min(40, len(c.in))])
		body := []byte(c.in)
		for split := 0; split <= len(body); split++ {
			var out bytes.Buffer
			r := NewRedactor(&out)
			_, _ = r.Write(body[:split])
			_, _ = r.Write(body[split:])
			require.NoError(t, r.Close())
			require.Equal(t, c.want, out.String(), "split at %d", split)
			assert.LessOrEqual(t, len(r.pending), 0)
		}
	}
	// Memory stays flat: a token far longer than any ceiling, in small writes.
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, _ = r.Write([]byte("Bearer "))
	for i := 0; i < 2000; i++ {
		_, _ = r.Write([]byte(fake("", 64, alnum)))
		assert.LessOrEqual(t, len(r.pending), 600)
	}
	_, _ = r.Write([]byte(" end"))
	require.NoError(t, r.Close())
	assert.Equal(t, "Bearer "+m+" end", out.String())
}

// SHOULD 2: PGP private keys; NIT 4: a bare Authorization value needs a digit or a symbol.
func TestRedactPGPAndBareAuthorization(t *testing.T) {
	m := RedactMarker
	pgp := "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\n" + fake("", 64, alnum) + "\n=abcd\n-----END PGP PRIVATE KEY BLOCK-----"
	assert.Equal(t, "a\n"+m+"\nb", string(RedactTokens([]byte("a\n"+pgp+"\nb"))))
	pub := "-----BEGIN PGP PUBLIC KEY BLOCK-----\nx\n-----END PGP PUBLIC KEY BLOCK-----"
	assert.Equal(t, pub, string(RedactTokens([]byte(pub))))
	for split := 0; split <= len(pgp); split++ {
		var out bytes.Buffer
		r := NewRedactor(&out)
		_, _ = r.Write([]byte(pgp[:split]))
		_, _ = r.Write([]byte(pgp[split:]))
		require.NoError(t, r.Close())
		require.Equal(t, m, out.String(), "split at %d", split)
	}
	for in, want := range map[string]string{
		"Authorization: unavailable":                 "Authorization: unavailable",
		"authorization=forbiddenwhenoff":             "authorization=forbiddenwhenoff",
		"Authorization: " + fake("", 30, alnum):      "Authorization: " + m,
		"Authorization: Basic onlyletterscredential": "Authorization: Basic " + m, // a scheme word: any value
	} {
		assert.Equal(t, want, string(RedactTokens([]byte(in))), in)
	}
}
