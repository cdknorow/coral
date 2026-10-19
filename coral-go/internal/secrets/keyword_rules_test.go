package secrets

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5132: a value after a recognised keyword is redacted whatever its entropy. coral-hub measured the misses of the entropy-gated library on 2000
// fresh random samples per shape; this is the same measure, on the redactor Coral runs, with a fixed seed: every shape must have ZERO misses.
// The values are built at run time (nothing in the repository looks like a real secret).
const samples = 2000

var (
	hexAlpha    = "0123456789abcdef"
	b64Alpha    = alnumAZ + "+/"
	b64urlAlpha = alnumAZ + "-_"
	alnumLower  = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// valueShape is how a shape's random value is made: its alphabet and a length drawn from min..max.
type valueShape struct {
	alphabet string
	min, max int
}

func (v valueShape) make(r *rand.Rand) string {
	n := v.min + r.Intn(v.max-v.min+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = v.alphabet[r.Intn(len(v.alphabet))]
	}
	return string(b)
}

type shape struct {
	name string
	text func(value string) string // the text the secret is in; value is what must not survive
	val  valueShape
}

func shapes() []shape {
	hex32 := valueShape{hexAlpha, 32, 32}
	hex40 := valueShape{hexAlpha, 40, 40}
	tok := valueShape{alnumAZ, 20, 64}
	var out []shape
	add := func(name string, val valueShape, text func(string) string) { out = append(out, shape{name, text, val}) }
	// A key in a query string: at the end, before &, before a space or a quote.
	add("query api_key at the end", hex32, func(v string) string { return "GET /v1/items?limit=10&api_key=" + v })
	add("query api_key before &", hex32, func(v string) string { return "GET /v1/items?api_key=" + v + "&limit=10 HTTP/1.1" })
	add("query apikey base64ish", valueShape{b64urlAlpha, 24, 48}, func(v string) string { return "https://api.example.com/data?apikey=" + v + "&format=json" })
	add("query token", valueShape{alnumAZ, 20, 40}, func(v string) string { return "curl 'https://x.example.com/hook?token=" + v + "'" })
	add("query access_token", hex40, func(v string) string { return "/oauth/me?access_token=" + v })
	add("query key=", hex32, func(v string) string { return "https://maps.example.com/api?q=a&key=" + v + "&z=3" })
	add("x-api-key header", tok, func(v string) string { return "x-api-key: " + v })
	add("curl -H x-api-key", tok, func(v string) string { return `curl -H "X-API-Key: ` + v + `" https://api.example.com/` })
	add("aws secret access key", valueShape{b64Alpha, 40, 40}, func(v string) string { return "aws_secret_access_key=" + v })
	add("AWS_SECRET_ACCESS_KEY spaced", valueShape{b64Alpha, 40, 40}, func(v string) string { return `AWS_SECRET_ACCESS_KEY = "` + v + `"` })
	add("aws config file", valueShape{b64Alpha, 40, 40}, func(v string) string { return "[default]\naws_secret_access_key = " + v + "\nregion = us-east-1" })
	add("curl -u user:pass", valueShape{alnumAZ, 8, 24}, func(v string) string { return "curl -u admin:" + v + " https://db.example.com/_all_dbs" })
	add("curl --user", valueShape{alnumAZ + "!@#%^*", 8, 24}, func(v string) string { return "curl --user bob:" + v + " -X POST https://api.example.com/x" })
	add("key without its marker (OPENAI_API_KEY)", valueShape{alnumAZ, 48, 51}, func(v string) string { return "OPENAI_API_KEY=" + v })
	add("api_key assignment", hex32, func(v string) string { return "api_key = " + v })
	add("api_key quoted", tok, func(v string) string { return `api_key = "` + v + `"` })
	add("apikey colon", tok, func(v string) string { return "apikey: " + v })
	add("export GITHUB_TOKEN", hex40, func(v string) string { return "export GITHUB_TOKEN=" + v })
	add("export NAME_TOKEN quoted", tok, func(v string) string { return `export DEPLOY_TOKEN="` + v + `"` })
	add("export STRIPE_SECRET", tok, func(v string) string { return "export STRIPE_SECRET=" + v })
	add("export DB_PASSWORD", valueShape{alnumAZ, 10, 30}, func(v string) string { return "export DB_PASSWORD=" + v })
	add("export MY_API_KEY", tok, func(v string) string { return "export MY_API_KEY=" + v })
	add("password=", valueShape{alnumAZ, 10, 30}, func(v string) string { return "psql -h db password=" + v })
	add("passwd:", valueShape{alnumAZ, 10, 30}, func(v string) string { return `passwd: "` + v + `"` })
	add("secret=", tok, func(v string) string { return "client_secret=" + v })
	add("token=", tok, func(v string) string { return "auth_token=" + v })
	add("Authorization Bearer", tok, func(v string) string { return "Authorization: Bearer " + v })
	add("Authorization Bearer, JSON", tok, func(v string) string { return `{"headers": {"Authorization": "Bearer ` + v + `"}}` })
	add("Authorization Basic", valueShape{b64Alpha, 16, 40}, func(v string) string { return "Authorization: Basic " + v + "==" })
	add("env block, many lines", tok, func(v string) string { return "PATH=/usr/bin\nHOME=/home/a\nSERVICE_TOKEN=" + v + "\nLANG=C" })
	return out
}

// miss reports whether the secret survives the redactor in text.
func miss(t *testing.T, text, secret string) bool {
	out, err := hubRedactor().Text([]byte(text))
	require.NoError(t, err)
	return strings.Contains(string(out), secret)
}

func TestKeywordValuesAreRedactedWhateverTheirEntropy(t *testing.T) {
	require.NoError(t, Init())
	for _, sh := range shapes() {
		t.Run(sh.name, func(t *testing.T) {
			r := rand.New(rand.NewSource(5132))
			misses := 0
			var first string
			for i := 0; i < samples; i++ {
				v := sh.val.make(r)
				if miss(t, sh.text(v), v) {
					misses++
					if first == "" {
						first = sh.text(v)
					}
				}
			}
			assert.Zero(t, misses, "%d of %d missed, the first: %q", misses, samples, first)
		})
	}
}

// What the keyword rules leave alone (the rules' own spans, not the library's: betterleaks has rules of its own that redact some of these, as
// "password: required"): ordinary text with the same words, short values, placeholders, names inside other words (the tradeoffs the rules' comment
// and agent_docs/redaction.md state).
func TestKeywordRulesLeaveProseAndPlaceholdersAlone(t *testing.T) {
	for _, text := range []string{
		"the token expires after an hour",
		"tokens: 5 of 10 used",
		"token=abc",
		"password=",
		"password: required",
		"password: correcthorse",
		"secret: none",
		"secret=true",
		"the secretary=alice1234567890123456 is here",
		"mytoken=abcdefghijklmnopqrstuvwx",
		"passwordless login is on, password=short",
		"api_key=$API_KEY_FROM_ENV_VARIABLE_X",
		"export GITHUB_TOKEN=${GITHUB_TOKEN_VALUE_HERE}",
		"export API_KEY=<your-api-key-goes-here>",
		"api_key=****************",
		"api_key=xxxxxxxxxxxxxxxxxxxx",
		`{"token": "{{ token_value_here }}"}`,
		"GET /list?page=2&sort=asc&limit=100",
		"/search?key=short&token=abc",
		"https://example.com/docs?author=jsmith&sig=1",
		"SORT_KEY=ordering",
		"monkey=banana_split_extra_long_value",
		"the author=somebodywithaverylongname wrote it",
		"curl https://example.com -H 'Accept: application/json'",
		"curl -u admin https://example.com",
		"curl -u admin:$PASSWORD https://example.com",
		"curl -u admin:<password> https://example.com",
		"curl -u admin:${DB_PASS} https://example.com",
		"Authorization: Bearer",
		"Authorization: Bearer short",
		"a bearer token is required",
		"keyword: token, value: none",
		"api_key: abcdefghijklmno",
	} {
		assert.Empty(t, keywordSpans(text), "left alone: %q", text)
	}
}

// The thresholds, at the edge: the value's length decides, and a password of 7 characters is the documented gap.
func TestKeywordRulesMinimumLengths(t *testing.T) {
	for _, c := range []struct {
		text   string
		redact bool
	}{
		{"api_key=" + strings.Repeat("a1", 8), true},           // 16: a key
		{"api_key=" + strings.Repeat("a1", 7) + "a", false},    // 15
		{"password=" + "Pa55w0rd", true},                       // 8
		{"password=" + "Pa55w0r", false},                       // 7: the documented gap
		{"curl -u bob:abcd https://h/x", true},                 // 4
		{"curl -u bob:abc https://h/x", false},                 // 3
		{"?token=" + "0123456789ab", true},                     // 12 in a query string
		{"?token=" + "0123456789a", false},                     // 11
		{"Authorization: Bearer abcdefghijkl", true},           // 12
		{"Authorization: Bearer abcdefghijk", false},           // 11
		{"export STRIPE_KEY=" + strings.Repeat("a1", 8), true}, // 16 for an env variable
		{"export STRIPE_KEY=" + strings.Repeat("a1", 7), false},
	} {
		assert.Equal(t, c.redact, len(keywordSpans(c.text)) > 0, c.text)
	}
}

// What surrounds the value stays: the name, the separator and what follows the value.
func TestKeywordRulesKeepTheTextAroundTheValue(t *testing.T) {
	require.NoError(t, Init())
	v := "f581aa86faef2f149bf4b901156baf59"
	for text, want := range map[string]string{
		"GET /v1/items?api_key=" + v + "&limit=10 HTTP/1.1": "GET /v1/items?api_key=[redacted token]&limit=10 HTTP/1.1",
		"export GITHUB_TOKEN=" + v + "\necho done":          "export GITHUB_TOKEN=[redacted token]\necho done",
		`{"api_key": "` + v + `", "name": "x"}`:             `{"api_key": "[redacted token]", "name": "x"}`,
		"curl -u admin:S3cretpw! https://h/x -X POST":       "curl -u admin:[redacted token] https://h/x -X POST",
	} {
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.Equal(t, want, string(out))
	}
}

// A secret split across the scanner's chunks (a long text, the secret on the edge) is still found, at every offset of a window of positions.
func TestKeywordValuesAreFoundWhereverTheyFallInALongText(t *testing.T) {
	require.NoError(t, Init())
	secret := "f581aa86faef2f149bf4b901156baf59"
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 1)
	for _, off := range []int{0, 1, 77, 4000, 8191, 8192, 8193, 16000, 65530, 65536, 65540, 100000} {
		text := strings.Repeat(pad, off/len(pad)+1)[:off] + "\napi_key=" + secret + "&x=1\n" + strings.Repeat(pad, 20)
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.NotContains(t, string(out), secret, "offset %d", off)
	}
}

// A password may hold the characters that end a query parameter or a list: its tail is not left showing (to a space or a quote); a key stops at them.
func TestPasswordWithSeparatorsIsRedactedToItsEnd(t *testing.T) {
	require.NoError(t, Init())
	out, err := hubRedactor().Text([]byte("login password=Ab&cd;ef,gh1234xyz rest"))
	require.NoError(t, err)
	assert.Equal(t, "login password=[redacted token] rest", string(out))
	out, err = hubRedactor().Text([]byte("GET /x?api_key=f581aa86faef2f149bf4b901156baf59&b=1;c=2"))
	require.NoError(t, err)
	assert.Equal(t, "GET /x?api_key=[redacted token]&b=1;c=2", string(out), "a key stops at & and ;")
}
