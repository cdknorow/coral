package redact

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3756: a URL's password (scheme://user:password@host) is redacted; the scheme, the user and the
// host stay, so the line still reads. Raw, as a JSON string and as JSON inside JSON, at every split
// into two writes. URLs without a password, emails and ssh's git@host:path are left alone.

// urlSecretShown is the longest run (4 bytes or more) of secret found in out, or "".
func urlSecretShown(out, secret string) string {
	for n := len(secret); n >= 4; n-- {
		for i := 0; i+n <= len(secret); i++ {
			if strings.Contains(out, secret[i:i+n]) {
				return secret[i : i+n]
			}
		}
	}
	return ""
}

func urlRedactTwoWrites(t *testing.T, body []byte, k int) string {
	t.Helper()
	var out bytes.Buffer
	r := NewWriter(&out)
	_, err := r.Write(body[:k])
	require.NoError(t, err)
	_, err = r.Write(body[k:])
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return out.String()
}

func TestRedactURLPasswords(t *testing.T) {
	pass := fake("", 20, alnum)
	for _, c := range []struct{ url, user, secret, host string }{
		{"postgres://app:" + pass + "@db.internal:5432/main", "app", pass, "@db.internal:5432/main"},
		{"postgresql://app:" + pass + "@db/main?sslmode=require", "app", pass, "@db/main"},
		{"mysql://root:" + pass + "@127.0.0.1:3306/x", "root", pass, "@127.0.0.1:3306/x"},
		{"redis://:" + pass + "@cache:6379/0", "", pass, "@cache:6379/0"},
		{"rediss://default:" + pass + "@cache.example:6380", "default", pass, "@cache.example:6380"},
		{"mongodb://admin:" + pass + "@m1,m2,m3/db?replicaSet=rs0", "admin", pass, "@m1,m2,m3/db"},
		{"mongodb+srv://admin:" + pass + "@cluster0.example.net/db", "admin", pass, "@cluster0.example.net/db"},
		{"amqp://guest:" + pass + "@rabbit:5672/vhost", "guest", pass, "@rabbit:5672/vhost"},
		{"amqps://guest:" + pass + "@rabbit", "guest", pass, "@rabbit"},
		{"https://bob:" + pass + "@example.com/repo.git", "bob", pass, "@example.com/repo.git"},
		{"git+https://bob:" + pass + "@git.example.com/x", "bob", pass, "@git.example.com/x"},
		{"ftp://u:" + pass + "@files.example", "u", pass, "@files.example"},
		{"postgres://app:p@ss:w0rd" + pass[:8] + "@db/main", "app", "p@ss:w0rd" + pass[:8], "@db/main"},
		{"postgres://app:pa%40ss" + pass[:10] + "@db", "app", "pa%40ss" + pass[:10], "@db"},
	} {
		forms := []string{
			"connecting to " + c.url + " now",
			"DATABASE_URL=" + c.url,
			"export REDIS_URL='" + c.url + "'",
			`{"dsn": "` + c.url + `"}`,
		}
		for _, text := range forms {
			js, _ := json.Marshal(map[string]string{"content": text})
			jsjs, _ := json.Marshal(map[string]string{"content": string(js)})
			for enc, body := range map[string][]byte{"raw": []byte(text), "json": js, "json-in-json": jsjs} {
				whole := string(Tokens(body))
				assert.Empty(t, urlSecretShown(whole, c.secret), "%s (%s): %s", text, enc, whole)
				if enc == "raw" {
					scheme := c.url[:strings.Index(c.url, "://")+3]
					assert.Contains(t, whole, scheme+c.user+":"+Marker+c.host, "scheme, user and host stay: %s", whole)
				} else {
					assert.True(t, json.Valid([]byte(whole)), "%s (%s) stays JSON: %s", text, enc, whole)
				}
				for k := 0; k <= len(body); k++ {
					if got := urlRedactTwoWrites(t, body, k); got != whole {
						t.Errorf("%s (%s) split at %d: %q, whole %q", text, enc, k, got, whole)
						break
					}
				}
			}
		}
	}
}

// URLs and addresses without a password are left as they are, raw and in JSON.
func TestRedactURLNearMisses(t *testing.T) {
	for _, text := range []string{
		"cloned git@github.com:org/repo.git",
		"ssh://git@github.com:22/org/repo",
		"mail ops at a.b@example.com about it",
		"https://example.com:8443/path?q=1",
		"https://user@host.example/x",
		"http://host.example/@someone",
		"see https://host.example/a:b@c",
		"file:///tmp/x:y@z",
		"postgres://app:@db/main",
		"redis://cache:6379",
		"time 10:30 at 5://x",
	} {
		assert.Equal(t, text, string(Tokens([]byte(text))), text)
		js, _ := json.Marshal(map[string]string{"content": text})
		assert.Equal(t, string(js), string(Tokens(js)), "%s in JSON", text)
	}
}

// Security's property (#3731), with URL atoms: any JSON string built from them, split anywhere,
// is still JSON after redaction and the same as unsplit.
func TestRedactURLJSONStaysValid(t *testing.T) {
	rng := rand.New(rand.NewSource(3756))
	atoms := []string{"://", "postgres", "https", "user", ":", "@", "p@ss", "host", "/", "\"", "\\", " ", "\n", "'", "?", "#", "abc123", "é", "%40", "{", "}", "=", "redis://:", "git@github.com:"}
	bad := 0
	for it := 0; it < 40000 && bad < 5; it++ {
		var sb strings.Builder
		for i, n := 0, 2+rng.Intn(16); i < n; i++ {
			sb.WriteString(atoms[rng.Intn(len(atoms))])
		}
		b, _ := json.Marshal(map[string]string{"content": sb.String()})
		in := string(b)
		split := rng.Intn(len(in) + 1)
		var out bytes.Buffer
		r := NewWriter(&out)
		_, _ = r.Write(b[:split])
		_, _ = r.Write(b[split:])
		_ = r.Close()
		if !json.Valid(out.Bytes()) {
			t.Errorf("INVALID JSON in=%q out=%q", in, out.String())
			bad++
		}
		if whole := string(Tokens(b)); whole != out.String() {
			t.Errorf("SPLIT DIFFERS in=%q whole=%q split=%q", in, whole, out.String())
			bad++
		}
	}
}
