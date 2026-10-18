package redact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Security's probes on #3756 (p3731/sec_url_test.go, sec_url2_test.go), as tests.
func TestSecURLProbes(t *testing.T) {
	const pw = "Zq8xK2mP9vL4"
	for _, r := range []string{
		`postgres://app:` + pw + `@db.internal:5432/main`,
		`mongodb+srv://u:` + pw + `@cluster0.abc.mongodb.net/?retryWrites=true`,
		`redis://:` + pw + `@cache:6379`,
		`postgres://u:ab/` + pw + `@h/db`,
		`postgres://u:ab?` + pw + `@h/db`,
		`postgres://u:ab#` + pw + `@h/db`,
		`DATABASE_URL="mysql://root:` + pw + `@127.0.0.1:3306/app"`,
		`curl https://user:` + pw + `@api.example.com/v1`,
		`git clone https://oauth2:` + pw + `@gitlab.com/a/b.git`,
		`amqp://guest:` + pw + `@rabbit:5672/%2f`,
		`https://u:` + pw + `@h` + strings.Repeat("a", 1100),
		`postgres://u:` + pw + `@h/db password: ok`,
		`postgres://u:1234` + pw + `@h/db`,
	} {
		out := string(Tokens([]byte(r)))
		assert.NotContains(t, out, "Zq8x", "%.60q -> %.100q", r, out)
	}
	assert.NotContains(t, string(Tokens([]byte(`postgres://u:p%40ss%2Fw0rd@h/db`))), "p%40ss%2Fw0rd")
	for _, r := range []string{
		`ssh://git@github.com:22/org/repo`, `https://host:8443/path`, `https://user@host/x`, `redis://cache:6379`,
		`mail me at a@b.com: see http://x.y/z`, `https://h:80/x/@user`, `https://h:443?x=@y`,
	} {
		assert.Equal(t, r, string(Tokens([]byte(r))), "left alone: %s", r)
	}
	for _, r := range []string{
		`{"url":"postgres://u:` + pw + `@h/db"}`,
		`{"cmd":"psql \"postgres://u:` + pw + `@h/db\" -c x"}`,
		`{"a":"{\"u\":\"redis://:` + pw + `@c\"}"}`,
	} {
		out := Tokens([]byte(r))
		assert.NotContains(t, string(out), "Zq8x", "%s -> %s", r, out)
		assert.True(t, json.Valid(out), "%s", out)
	}
}

// urlAdversarial are inputs that made the URL rule's look-ahead quadratic (Security on #3756 v2:
// "://" repeated over 4 MiB took 4.1 s; now well under 100 ms on the Mac).
var urlAdversarial = []string{"://", "a://:", "://u:", "://:" + strings.Repeat("x", 1000), "://:x@",
	"://:" + strings.Repeat("x", 1000) + "@", "@:@:", strings.Repeat("a", 900) + "://:"}

// Adversarial input stays linear. The URL rule's work through Tokens is counted, not timed: urlScanned
// (#4908) sums the bytes the finder's helpers examine (the "://" search, the user, the password's run
// and its port check), so there is no clock and so no load band to fall outside -- a timing ratio of
// 1 vs 4 MiB flaked on epoch at load 32-54 even best-of-N, a spike landing on the large runs only. For
// each adversarial unit the count is a small constant times the text and 4x the text is about 4x the
// count; a look-ahead that rescans from each "://" would count hundreds of times the text and fail at
// once. BenchmarkRedactURLAdversarial gives the real time. (Mirrors #4908's work-count test, but through
// Tokens -- the writer's URL rule path, not URLPasswordSpans.)
func TestSecURLPerf(t *testing.T) {
	work := func(data []byte) int {
		n := 0
		urlScanned = &n
		defer func() { urlScanned = nil }()
		Tokens(data)
		return n
	}
	for _, unit := range urlAdversarial {
		if !strings.Contains(unit, "://") {
			continue // no URL: the finder does no counted work (a non-match, left to TestSecURLProbes); nothing to be quadratic in
		}
		small := []byte(strings.Repeat(unit, 1<<20/len(unit)))
		large := []byte(strings.Repeat(unit, 4<<20/len(unit)))
		ws, wl := work(small), work(large)
		require.NotZero(t, ws, "%.20q: the finder did some work", unit)
		ratio := float64(wl) / float64(ws)
		t.Logf("%.20q: 1 MiB work %d, 4 MiB work %d, bytes/byte %.2f, ratio %.2f×", unit, ws, wl, float64(wl)/float64(len(large)), ratio)
		assert.LessOrEqual(t, wl, 3*len(large), "unit %.20q: the finder examines at most 3 bytes per byte of text (examined %d for %d)", unit, wl, len(large))
		assert.Less(t, ratio, 5.0, "unit %.20q: 4x the text is %.2f× the work (linear is about 4×, a quadratic finder much more)", unit, ratio)
	}
}

func BenchmarkRedactURLAdversarial(b *testing.B) {
	for _, unit := range urlAdversarial {
		data := []byte(strings.Repeat(unit, 1<<20/len(unit)))
		b.Run(fmt.Sprintf("%.8q", unit), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				Tokens(data)
			}
		})
	}
}

// Long lines of a wide rule's characters (CSV, a hex dump, minified JSON) are not cut to nothing:
// the walk back stops after maxWideWalk. A run of a token's own characters only (base64) is a
// token's, and the cut still goes back to its start.
func TestSecURLCut(t *testing.T) {
	for name, s := range map[string]string{
		"csv":     strings.Repeat("a,b;c(d)", 600),
		"hexdump": strings.Repeat("0123456789abcdef:", 300),
		"minjson": strings.Repeat(`{"k":1},`, 600),
	} {
		p := Cut(s, 2048)
		assert.GreaterOrEqual(t, p, 2048-maxWideWalk-1, "%s: cut at %d", name, p)
	}
	assert.Equal(t, 0, Cut(strings.Repeat("QUJDREVGR0hJSktMTU5PUA==", 200), 2048), "base64: a token's run, cut before it")
}
