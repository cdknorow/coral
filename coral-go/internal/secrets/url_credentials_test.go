package secrets

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// #4769: a URL's password (scheme://user:password@host) reached Coral Hub viewers: betterleaks has no rule for it, and the writer's
// (pkg/redact.Rules, #3756) is not the connector's. The connector's redactor now masks it, in text, in a JSON string and in JSON inside JSON,
// as one whole or written in two parts at every split, and leaves the scheme, the user and the host showing, as the writer's rule did.

// shown is the longest run (4 bytes or more) of secret found in out, or "".
func shownRun(out, secret string) string {
	for n := len(secret); n >= 4; n-- {
		for i := 0; i+n <= len(secret); i++ {
			if strings.Contains(out, secret[i:i+n]) {
				return secret[i : i+n]
			}
		}
	}
	return ""
}

func twoWrites(t *testing.T, body []byte, k int) string {
	t.Helper()
	var out bytes.Buffer
	st := Redactor().NewStream(&out)
	_, err := st.Write(body[:k])
	require.NoError(t, err)
	_, err = st.Write(body[k:])
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return out.String()
}

func TestURLPasswordsAreMasked(t *testing.T) {
	require.NoError(t, Init())
	pass := fake(20, alnumAZ, 11)
	for _, c := range []struct{ url, user, secret, host string }{
		{"postgres://app:" + pass + "@db.internal:5432/main", "app", pass, "@db.internal:5432/main"},
		{"postgresql://app:" + pass + "@db/main?sslmode=require", "app", pass, "@db/main"},
		{"mysql://root:" + pass + "@127.0.0.1:3306/x", "root", pass, "@127.0.0.1:3306/x"},
		{"redis://:" + pass + "@cache:6379/0", "", pass, "@cache:6379/0"},
		{"amqps://guest:" + pass + "@rabbit", "guest", pass, "@rabbit"},
		{"https://bob:" + pass + "@example.com/repo.git", "bob", pass, "@example.com/repo.git"},
		{"git+https://bob:" + pass + "@git.example.com/x", "bob", pass, "@git.example.com/x"},
		{"ftp://u:" + pass + "@files.example", "u", pass, "@files.example"},
		// a password holding @ and :, a percent-encoded one, a short weak one, an all-digit one
		{"postgres://app:p@ss:w0rd" + pass[:8] + "@db/main", "app", "p@ss:w0rd" + pass[:8], "@db/main"},
		{"postgres://app:pa%40ss" + pass[:10] + "@db", "app", "pa%40ss" + pass[:10], "@db"},
		{"https://user:hunter2@host.example/x", "user", "hunter2", "@host.example/x"},
		{"https://user:123456@host.example/x", "user", "123456", "@host.example/x"},
		// an IPv6 host
		{"http://svc:" + pass + "@[2001:db8::1]:8080/x", "svc", pass, "@[2001:db8::1]:8080/x"},
		{"http://svc:" + pass + "@[::1]/x", "svc", pass, "@[::1]/x"},
		// a password with a raw / ? #
		{"https://u:" + pass[:6] + "/" + pass[6:12] + "?" + pass[12:] + "@h.example/p", "u", pass[:6] + "/" + pass[6:12] + "?" + pass[12:], "@h.example/p"},
	} {
		forms := []string{
			"connecting to " + c.url + " now",
			"DATABASE_URL=" + c.url,
			"export REDIS_URL='" + c.url + "'",
			`{"dsn": "` + c.url + `"}`,
			"Remote: " + c.url + "\n",
			`"` + c.url + `" and "` + c.url + `"`, // quoted, twice on one line
		}
		for _, text := range forms {
			js, _ := json.Marshal(map[string]string{"content": text})
			jsjs, _ := json.Marshal(map[string]string{"content": string(js)})
			for enc, body := range map[string][]byte{"raw": []byte(text), "json": js, "json-in-json": jsjs} {
				var whole string
				if enc == "raw" {
					out, err := Redactor().Text(body)
					require.NoError(t, err)
					whole = string(out)
				} else {
					out, err := Redactor().Reply(body, true)
					require.NoError(t, err)
					whole = string(out)
					assert.True(t, json.Valid(out), "%s (%s) stays JSON: %s", text, enc, whole)
				}
				assert.Empty(t, shownRun(whole, c.secret), "%s (%s): %s", text, enc, whole)
				if enc == "raw" {
					scheme := c.url[:strings.Index(c.url, "://")+3]
					assert.Contains(t, whole, scheme+c.user+":"+redact.Marker+c.host, "scheme, user and host stay: %s", whole)
				}
			}
			// written in two parts, at every split (raw text through the stream the connector uses)
			whole, err := Redactor().Text([]byte(text))
			require.NoError(t, err)
			for k := 0; k <= len(text); k++ {
				got := twoWrites(t, []byte(text), k)
				assert.Empty(t, shownRun(got, c.secret), "%s split at %d: %q", text, k, got)
				if got != string(whole) {
					t.Errorf("%s split at %d: %q, whole %q", text, k, got, whole)
					break
				}
			}
		}
	}
}

// betterleaks already masks a whole mongodb+srv:// connection string (its own rule, as one marker): the password is gone either way.
func TestURLPasswordInAConnectionStringTheLibraryMasksWhole(t *testing.T) {
	require.NoError(t, Init())
	pass := fake(20, alnumAZ, 13)
	out, err := Redactor().Text([]byte("use mongodb+srv://admin:" + pass + "@cluster0.example.net/db now"))
	require.NoError(t, err)
	assert.Empty(t, shownRun(string(out), pass), string(out))
}

// A user name that is itself the credential (https://<token>@host, no password) is masked; an ordinary user, an email and ssh's git@host:path
// are not, and neither are URLs with no password or with a port where a password would be.
func TestURLUserTokenAndNearMisses(t *testing.T) {
	require.NoError(t, Init())
	tokUser := fake(40, alnumAZ, 12)
	out, err := Redactor().Text([]byte("clone https://" + tokUser + "@git.example.com/org/repo.git now"))
	require.NoError(t, err)
	assert.Empty(t, shownRun(string(out), tokUser), string(out))
	assert.Contains(t, string(out), "https://"+redact.Marker+"@git.example.com/org/repo.git")
	for _, text := range []string{
		"cloned git@github.com:org/repo.git",
		"ssh://git@github.com:22/org/repo",
		"mail ops at a.b@example.com about it",
		"https://example.com:8443/path?q=1",
		"https://user@host.example/x",
		"https://deploy-bot@host.example/x",
		"https://a-long-but-plain-name-here@host.example/x",
		"http://host.example/@someone",
		"see https://host.example/a:b@c",
		"file:///tmp/x:y@z",
		"postgres://app:@db/main",
		"redis://cache:6379",
		"http://[::1]:8080/path",
		"http://[fe80::1]/x",
		"time 10:30 at 5://x",
	} {
		got, err := Redactor().Text([]byte(text))
		require.NoError(t, err)
		assert.Equal(t, text, string(got), text)
		js, _ := json.Marshal(map[string]string{"content": text})
		gotJS, err := Redactor().Reply(js, true)
		require.NoError(t, err)
		assert.Equal(t, string(js), string(gotJS), "%s in JSON", text)
	}
}

// Several URLs on one line each lose their own password, and a line made of "://" is scanned in linear time.
func TestURLPasswordsOnOneLineAndBounded(t *testing.T) {
	require.NoError(t, Init())
	p1, p2, p3 := fake(16, alnumAZ, 21), fake(16, alnumAZ, 22), fake(16, alnumAZ, 23)
	line := "a https://x:" + p1 + "@h1 b redis://:" + p2 + "@h2:6379/0 c amqp://g:" + p3 + "@h3/v d https://plain.example/ e"
	got, err := Redactor().Text([]byte(line))
	require.NoError(t, err)
	for _, p := range []string{p1, p2, p3} {
		assert.Empty(t, shownRun(string(got), p), string(got))
	}
	assert.Contains(t, string(got), "https://plain.example/")
	assert.Equal(t, 3, strings.Count(string(got), redact.Marker))
	// Through the whole redactor, at sizes with 20x headroom to its 3 s budget: over budget is an error, and how long the rules of betterleaks
	// take on a megabyte is the machine's speed (this test took 3.1-3.6 s alone with 3 MiB, and crossed the budget under a full-module load,
	// #4828); it is not this rule's cost, which the next test pins by how it grows.
	for _, line := range []string{strings.Repeat("://", 1<<15), strings.Repeat("a://u:", 1<<13)} {
		_, err = Redactor().Text([]byte(line))
		assert.NoError(t, err, "%d bytes of repeated scheme separators are checked inside the budget", len(line))
	}
}

// The URL finder's linear time is tested in pkg/redact (TestURLPasswordSpansWorkIsLinear), by counting the bytes it examines, and its ceiling on a
// huge line (TestURLPasswordSpansFinishOnAHugeLine): the timing ratio that was here failed under a load of 32 to 54 on epoch (#4908).

// Property: any text built from URL atoms, written in two parts, is redacted as the whole is, and a JSON string built from them stays JSON.
func TestURLAtomsSplitAndJSONStaysValid(t *testing.T) {
	require.NoError(t, Init())
	rng := rand.New(rand.NewSource(4769))
	atoms := []string{"://", "postgres", "https", "user", ":", "@", "p@ss", "host", "/", "\"", "\\", " ", "\n", "'", "?", "#", "abc123", "é", "%40", "{", "}", "=", "redis://:", "git@github.com:", "[::1]", "8080"}
	for it := 0; it < 3000; it++ {
		var sb strings.Builder
		for i, n := 0, 2+rng.Intn(14); i < n; i++ {
			sb.WriteString(atoms[rng.Intn(len(atoms))])
		}
		text := sb.String()
		whole, err := Redactor().Text([]byte(text))
		require.NoError(t, err)
		k := rng.Intn(len(text) + 1)
		if got := twoWrites(t, []byte(text), k); got != string(whole) {
			t.Fatalf("%q split at %d: %q, whole %q", text, k, got, whole)
		}
		js, _ := json.Marshal(map[string]string{"content": text})
		out, err := Redactor().Reply(js, true)
		require.NoError(t, err)
		require.True(t, json.Valid(out), "%q -> %s", text, out)
	}
}
