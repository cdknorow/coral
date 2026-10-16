package tunnel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
		out := string(RedactTokens([]byte(r)))
		assert.NotContains(t, out, "Zq8x", "%.60q -> %.100q", r, out)
	}
	assert.NotContains(t, string(RedactTokens([]byte(`postgres://u:p%40ss%2Fw0rd@h/db`))), "p%40ss%2Fw0rd")
	for _, r := range []string{
		`ssh://git@github.com:22/org/repo`, `https://host:8443/path`, `https://user@host/x`, `redis://cache:6379`,
		`mail me at a@b.com: see http://x.y/z`, `https://h:80/x/@user`, `https://h:443?x=@y`,
	} {
		assert.Equal(t, r, string(RedactTokens([]byte(r))), "left alone: %s", r)
	}
	for _, r := range []string{
		`{"url":"postgres://u:` + pw + `@h/db"}`,
		`{"cmd":"psql \"postgres://u:` + pw + `@h/db\" -c x"}`,
		`{"a":"{\"u\":\"redis://:` + pw + `@c\"}"}`,
	} {
		out := RedactTokens([]byte(r))
		assert.NotContains(t, string(out), "Zq8x", "%s -> %s", r, out)
		assert.True(t, json.Valid(out), "%s", out)
	}
}

// urlAdversarial are inputs that made the URL rule's look-ahead quadratic (Security on #3756 v2:
// "://" repeated over 4 MiB took 4.1 s; now well under 100 ms on the Mac).
var urlAdversarial = []string{"://", "a://:", "://u:", "://:" + strings.Repeat("x", 1000), "://:x@",
	"://:" + strings.Repeat("x", 1000) + "@", "@:@:", strings.Repeat("a", 900) + "://:"}

// Adversarial input stays linear. The bound is loose (3 s for 4 MiB, against 11.6 s for the
// quadratic look-ahead) so slow machines pass, and the test is skipped under -race and -short,
// as wsredact_test.go's is; BenchmarkRedactURLAdversarial gives the real figure.
func TestSecURLPerf(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing bound: not under -race or -short (see BenchmarkRedactURLAdversarial)")
	}
	for _, unit := range urlAdversarial {
		data := []byte(strings.Repeat(unit, 4<<20/len(unit)))
		st := time.Now()
		RedactTokens(data)
		took := time.Since(st)
		t.Logf("%.20q: %v for 4 MiB", unit, took)
		assert.Less(t, took, 3*time.Second, "unit %.20q", unit)
	}
}

func BenchmarkRedactURLAdversarial(b *testing.B) {
	for _, unit := range urlAdversarial {
		data := []byte(strings.Repeat(unit, 1<<20/len(unit)))
		b.Run(fmt.Sprintf("%.8q", unit), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				RedactTokens(data)
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
		p := CutOutsideToken(s, 2048)
		assert.GreaterOrEqual(t, p, 2048-maxWideWalk-1, "%s: cut at %d", name, p)
	}
	assert.Equal(t, 0, CutOutsideToken(strings.Repeat("QUJDREVGR0hJSktMTU5PUA==", 200), 2048), "base64: a token's run, cut before it")
}
