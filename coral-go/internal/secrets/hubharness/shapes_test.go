// Package hubharness runs coral-hub's miss-rate harness against the redactor Coral Hub sees (secrets.HubRedactor).
//
// The shapes (shapes_test.go) are coral-hub's, from their harness (artifact coral://artifacts/64a76d960d88d1c868864f5ef5d1ef758e2e4963f82863b78cc13697a5ad9f62,
// the coral-hub team's code, used with their agreement); only the package name and this comment changed. harness_test.go is Coral's: it
// draws 2000 random samples per shape (the hub's own sample size) and requires zero that survive. The samples are SEEDED (#5943): the same seed draws the same secrets, so a miss can be run
// again; HUB_HARNESS_SEED picks another seed, HUB_HARNESS_SAMPLES another sample count (the helpers below were crypto/rand, the only other change to the hub's code).
package hubharness

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"strconv"
)

// defaultSeed is the seed of a plain run: a fixed number, so the harness draws the same secrets every time and a failure can be run again (#5943).
const defaultSeed = 5943

// harnessSeed is HUB_HARNESS_SEED (a number) or defaultSeed.
func harnessSeed() uint64 {
	if v := os.Getenv("HUB_HARNESS_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 0, 64)
		must(err)
		return n
	}
	return defaultSeed
}

var (
	chacha = func() *rand.ChaCha8 {
		var seed [32]byte
		binary.LittleEndian.PutUint64(seed[:], harnessSeed())
		return rand.NewChaCha8(seed)
	}()
	rng = rand.New(chacha)
)

type T struct{}

func (T) Helper() {}
func must(err error) {
	if err != nil {
		panic(err)
	}
}

const (
	alnum    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	b64url   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
)

func randFrom(t *T, set string, n int) string {
	t.Helper()
	out := make([]byte, n)
	for i := range out {
		out[i] = set[rng.IntN(len(set))]
	}
	return string(out)
}

func randHexStr(t *T, n int) string {
	t.Helper()
	b := make([]byte, (n+1)/2)
	_, err := chacha.Read(b)
	must(err)
	return hex.EncodeToString(b)[:n]
}

func randB64(t *T, nbytes int) string {
	t.Helper()
	b := make([]byte, nbytes)
	_, err := chacha.Read(b)
	must(err)
	return base64.StdEncoding.EncodeToString(b)
}

// leakShape is one fixture: text is what is written on the board, secret is the random part that must not come back.
type leakShape struct {
	name, text, secret string
	known              string // non-empty: a KNOWN GAP of the connector's redaction, the reason
}

func leakShapes(t *T) []leakShape {
	r := func(set string, n int) string { return randFrom(t, set, n) }
	const gap = "Security's betterleaks read (2026-10-16): the old rules caught this 20/20, betterleaks 0/20; coral-go was asked for a union detector with Authorization, URL userinfo and secret-named query rules"
	var s []leakShape
	add := func(name, prefix, secret, suffix, known string) {
		s = append(s, leakShape{name: name, text: prefix + secret + suffix, secret: secret, known: known})
	}
	// covered by both the old rules and betterleaks
	add("github token", "key is gh"+"p_", r(alnum, 36), " ok", "")
	add("github fine-grained token", "pat github_"+"pat_", r(alnum+"_", 82), " end", "")
	add("anthropic key", "ANTHROPIC "+"sk-ant-api03-", r(b64url, 93)+"AA", "", "") // a real key is api03- + 93 characters + "AA"
	add("openai project key with the marker", "OPENAI_API_KEY=sk-"+"proj-", r(alnum, 20)+"T3BlbkFJ"+r(alnum, 20), "", "")
	add("aws secret access key", "aws_secret_access_key = ", r(alnum, 40), "", "")
	add("slack bot token", "slack xox"+"b-", r("0123456789", 12)+"-"+r("0123456789", 12)+"-"+r(alnum, 24), "", "")
	add("db password assignment", "DB_PASSWORD=", r(alnum, 18), "", "")
	add("json password", `{"password": "`, r(alnum, 20), `"}`, "")
	add("api_key assignment", "api_key=", r(alnum, 32), "", "")
	add("git clone with x-access-token", "git clone https://x-access-token:", r(alnum, 36), "@github.com/org/repo.git", "")
	add("redis url password", "redis://:", r(alnum, 20), "@cache.internal:6379", "")
	add("mysql root url password", "mysql://root:", r(alnum, 20), "@db.internal/app", "")
	add("x-api-key header", "X-Api-Key: ", r(alnum, 32), "", "")
	add("bearer with a github token", "Authorization: Bearer gh"+"p_", r(alnum, 36), "", "")
	add("export github token", "export GITHUB_TOKEN=", r(alnum, 40), "", "")
	pem := randB64(t, 1200)
	add("private key block", "-----BEGIN OPENSSH "+"PRIVATE KEY-----\n", pem, "\n-----END OPENSSH "+"PRIVATE KEY-----", "") // split: no scanner may see a literal
	// new in betterleaks
	add("google api key", "key AI"+"za", r(alnum+"-_", 35), "", "")
	add("stripe live key", "stripe sk_"+"live_", r(alnum, 24), "", "")
	add("digitalocean token", "doctl dop_"+"v1_", randHexStr(t, 64), "", "")
	add("jwt", "token eyJ"+"hbGciOiJIUzI1NiJ9.", "eyJ"+r(b64url, 40)+"."+r(b64url, 43), "", "")
	add("curl -u user:pass", "curl "+"-u deploy:", r(alnum, 20), " https://api.internal/x", "") // split: no scanner may see a literal
	// the connector's own coverage found by the real run (v1.4.61, Security's corrected list): these are HARD now
	add("authorization bearer in a curl -H", "curl -H 'Authorization: Bearer ", r(alnum, 40), "' https://api.acme-corp.io/x", "")
	add("postgres url password", "psql postgres://app:", r(alnum, 20), "@db.acme-corp.io/prod", "")
	add("https url userinfo password", "git remote add o https://alice:", r(alnum, 24), "@git.acme-corp.io/r.git", "")
	add("api_key at the end of a query string", "GET /v1/items?api_key=", randHexStr(t, 32), "", "")
	add("password at the end of a query string", "https://x.acme-corp.io/login?password=", r(alnum, 20), "", "")
	add("openai key without the marker (sk- + 48)", "OPENAI_API_KEY=sk-", r(alnum, 48), "", "")
	// round 4 (#5212): Security's neighbour leaks of the hub's keyword rules, and coral-hub's four deterministic misses. HARD: zero of 2000 may survive.
	add("url-encoded JSON in a query parameter", "GET /view?cfg=%7B%22api_key%22%3A%22", r(alnum, 32), "%22%7D", "")
	add("api_key as an XML element", "<config><api_key>", r(alnum, 32), "</api_key></config>", "")
	add("api_key as an HTML form field", `<form><input type="hidden" name="api_key" value="`, r(alnum, 32), `"></form>`, "")
	add("double-encoded entity quotes (&amp;quot;)", "&amp;quot;api_key&amp;quot;: &amp;quot;", r(alnum, 32), "&amp;quot;", "")
	add("entity quotes without the semicolon", "&quotapi_key&quot: &quot", r(alnum, 32), "&quot", "")
	add("password after an encoded colon and space", "https://x.acme-corp.io/login?password%3D%20", r(alnum, 20), "", "")
	add("curl -b with the cookie glued on", "curl -bsession=", r(alnum, 32), " https://api.acme-corp.io/x", "")
	add("curl -b with a quoted cookie, no space", `curl -b"session=`, r(alnum, 32), `" https://api.acme-corp.io/x`, "")
	add("a url-encoded key name", "GET /v1/items?api%5Fkey=", randHexStr(t, 32), "", "")
	add("docker login -p", "docker login -u deploy -p ", r(alnum, 24), " registry.acme-corp.io", "")
	// round 5 (#5242): coral-hub's two deterministic misses of v1.4.122, and the names beside them. HARD: zero of 2000 may survive.
	add("password after an entity-encoded equals (&#61;)", "login?user=a&password&#61;", r(alnum, 20), "", "")
	add("password after an entity-encoded equals (&#x3d;)", "login?user=a&password&#x3d;", r(alnum, 20), "", "")
	add("password after an entity-encoded equals (&equals;)", "login?user=a&password&equals;", r(alnum, 20), "", "")
	add("npm config set registry token, space separated", "npm config set //registry.npmjs.org/:_authToken ", r(alnum, 36), "", "")
	add("npm config set _authToken", "npm config set _authToken ", r(alnum, 36), "", "")
	add(".npmrc _authToken line", "//registry.npmjs.org/:_authToken=", r(alnum, 36), "", "")
	add(".npmrc _auth line", "_auth=", randB64(t, 24), "", "")
	add("pwd assignment", "pwd=", r(alnum, 18), "", "")
	// KNOWN GAPS (Security's corrected findings, 2026-10-16): the subtest runs and skips with the reason while the secret leaks
	add("bare Authorization: Bearer line, opaque token", "Authorization: Bearer ", r(alnum, 40), "", gap)
	add("bare Authorization: Basic line", "Authorization: Basic ", randB64(t, 24), "", gap)
	add("api_key followed by another parameter", "GET /v1/items?api_key=", randHexStr(t, 32), "&limit=5", gap)
	add("token followed by another parameter", "https://x.acme-corp.io/cb?token=", r(alnum, 32), "&a=1", gap)
	add("openai sk-proj key without the marker", "OPENAI_API_KEY=sk-proj-", r(b64url, 156), "", gap)
	add("secret: <40 hex> line", "secret: ", randHexStr(t, 40), "", "partial in Security's read (18/20) and leaked in the real-connector run: "+gap)
	add("SECRET_KEY assignment", "SECRET_KEY=", r(alnum, 40), "", "partial in Security's read (18/20): "+gap)
	// both detectors miss it
	add("cookie header", "Cookie: session=", randHexStr(t, 32), "", "missed by the old rules AND betterleaks (Security's read)")
	return s
}
