package secrets

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5141: the second round of keyword shapes: JSON inside a JSON string, URL-encoded, names written as one word, command-line arguments, Cookie
// headers and the full-width separator. Same bar as #5132: 2000 seeded samples per shape, zero survivors.

const (
	bs   = `\`
	ffEq = "\uFF1D"
	ffCo = "\uFF1A"
)

func shapes2() []shape {
	hex32 := valueShape{hexAlpha, 32, 32}
	tok := valueShape{alnumAZ, 20, 64}
	pw := valueShape{alnumAZ, 10, 30}
	enc := valueShape{alnumAZ + "-_", 20, 40}
	var out []shape
	add := func(name string, val valueShape, text func(string) string) { out = append(out, shape{name, text, val}) }
	// JSON inside a JSON string: the quotes are escaped, once or twice.
	q := bs + `"`
	qq := bs + bs + bs + `"`
	add("json-in-json api_key", tok, func(v string) string { return `{"log": "{` + q + `api_key` + q + `: ` + q + v + q + `}"}` })
	add("json-in-json password", pw, func(v string) string { return `{"log": "{` + q + `password` + q + `: ` + q + v + q + `}"}` })
	add("json-in-json token", tok, func(v string) string {
		return `{"log": "{` + q + `token` + q + `: ` + q + v + q + `, ` + q + `n` + q + `: 1}"}`
	})
	add("json-in-json double-escaped", tok, func(v string) string { return `{"a": "{"b": "` + qq + `api_key` + qq + `: ` + qq + v + qq + `}"}` })
	add("json-in-json client_secret", tok, func(v string) string { return `"` + q + `client_secret` + q + `:` + q + v + q + `"` })
	// URL-encoded.
	add("encoded api_key", hex32, func(v string) string { return "https://h.example.com/cb?state=1&api_key%3D" + v + "%26x%3D1" })
	add("encoded token", enc, func(v string) string { return "q=token%3D" + v })
	add("encoded access_token, twice", enc, func(v string) string { return "q=access_token%253D" + v })
	add("encoded password", pw, func(v string) string { return "form=user%3Dbob%26password%3D" + v + "%26ok%3D1" })
	add("nested in next=", enc, func(v string) string {
		return "https://a.example.com/login?next=https%3A%2F%2Fb.example.com%2Fcb%3Ftoken%3D" + v + "%26a%3D1"
	})
	add("nested in redirect_uri, api_key", hex32, func(v string) string {
		return "/auth?redirect_uri=https%3A%2F%2Fb.example.com%2Fcb%3Fapi_key%3D" + v + "&scope=read"
	})
	// A name written as one word.
	add("PGPASSWORD", pw, func(v string) string { return "PGPASSWORD=" + v + " psql -h db" })
	add("export PGPASSWORD quoted", pw, func(v string) string { return `export PGPASSWORD="` + v + `"` })
	add("MYSQL_PWD", pw, func(v string) string { return "MYSQL_PWD=" + v + " mysqldump db" })
	add("AWS_SECRET", tok, func(v string) string { return "AWS_SECRET=" + v })
	add("SSHPASS", pw, func(v string) string { return "SSHPASS=" + v + " sshpass -e ssh host" })
	add("SECRET_KEY_BASE", valueShape{hexAlpha, 64, 128}, func(v string) string { return "SECRET_KEY_BASE=" + v })
	add("REDISCLI_AUTH", pw, func(v string) string { return "REDISCLI_AUTH=" + v + " redis-cli ping" })
	// Command-line arguments.
	add("--password VALUE", pw, func(v string) string { return "mytool login --user bob --password " + v + " --verbose" })
	add("--password=VALUE", pw, func(v string) string { return "mytool login --password=" + v })
	add("--token VALUE", tok, func(v string) string { return "gh api --token " + v + " /user" })
	add("--api-key VALUE", tok, func(v string) string { return "tool --api-key " + v })
	add("--db-password VALUE", pw, func(v string) string { return "tool --db-password " + v })
	add("--password quoted", pw, func(v string) string { return `tool --password "` + v + `" run` })
	add("mysql -pVALUE", pw, func(v string) string { return "mysql -h db -u root -p" + v + " mydb" })
	add("mysqldump -pVALUE", pw, func(v string) string { return "mysqldump -u root -p" + v + " mydb > out.sql" })
	add("sshpass -p VALUE", pw, func(v string) string { return "sshpass -p " + v + " ssh bob@host" })
	// Cookie headers.
	add("Cookie session", valueShape{alnumAZ, 20, 40}, func(v string) string { return "Cookie: theme=dark; session=" + v + "; lang=en" })
	add("Cookie sessionid short name", valueShape{alnumLower, 12, 32}, func(v string) string { return "Cookie: sessionid=" + v })
	add("Cookie, odd name", valueShape{b64urlAlpha, 24, 48}, func(v string) string { return "Cookie: _app_x=" + v + "; a=1" })
	add("Set-Cookie", valueShape{alnumAZ, 20, 40}, func(v string) string { return "Set-Cookie: SID=" + v + "; Path=/; HttpOnly; Secure" })
	add("Set-Cookie JSON", valueShape{alnumAZ, 20, 40}, func(v string) string { return `{"headers": {"Set-Cookie": "auth=` + v + `; Path=/"}}` })
	add("curl -H Cookie", valueShape{alnumAZ, 20, 40}, func(v string) string { return `curl -H 'Cookie: JSESSIONID=` + v + `' https://x.example.com/` })
	// The full-width separator.
	add("full-width = api_key", tok, func(v string) string { return "api_key" + ffEq + v })
	add("full-width : password", pw, func(v string) string { return "password" + ffCo + ` "` + v + `"` })
	add("full-width = token spaced", tok, func(v string) string { return "token " + ffEq + " " + v })
	return out
}

func TestKeywordRound2ValuesAreRedacted(t *testing.T) {
	require.NoError(t, Init())
	for _, sh := range shapes2() {
		t.Run(sh.name, func(t *testing.T) {
			r := rand.New(rand.NewSource(5141))
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

// Prose, placeholders and names inside other words stay as they are, for every round-2 shape too (asserted on the rules' own spans).
func TestKeywordRound2LeavesProseAndPlaceholdersAlone(t *testing.T) {
	q := bs + `"`
	for _, text := range []string{
		"mytoken=abcdefghijklmnop1234", "secretary: abcdefghijklmnop1234", "tokens=5", "tokens%3D5", "mytoken%3Dabcdefghijklmnop1234",
		"a password is required", "--password is required to log in", "pass --token flag", "set the --password option",
		"tool --password --verbose", "tool --token -v", "tool --password $PASSWORD", "tool --token <token>", "tool --password ${PW}",
		"PGPASSWORD=$DB_PASS psql", "PGPASSWORD=<password>", "MYSQL_PWD=short", "AWS_SECRET=short",
		"mysql -p", "mysql -h db -p mydb", "mysql -p$PASS db", "mysql --help",
		"sshpass -p $PW ssh host", "sshpass -e ssh host",
		"Cookie: theme=dark; lang=en", "Set-Cookie: a=1; Path=/; Domain=example.com; Max-Age=3600; HttpOnly; Secure; SameSite=Lax",
		"Cookie: expires=Tue, 20 Oct 2026 07:28:00 GMT", "the Cookie: header is optional", "cookie: yes",
		`{` + q + `api_key` + q + `: ` + q + `$KEY` + q + `}`, `{` + q + `password` + q + `: ` + q + `<password>` + q + `}`,
		"api_key%3D%24KEY", "token%3Dabc", "api_key" + ffEq + "short",
		"the " + ffEq + " sign and " + ffCo + " colon", "next=https%3A%2F%2Fexample.com%2Fhome",
	} {
		assert.Empty(t, keywordSpans(text), "%q", text)
	}
}

// The text around a redacted value is kept, in the escaped and encoded forms too.
func TestKeywordRound2KeepsTheTextAroundTheValue(t *testing.T) {
	require.NoError(t, Init())
	v := "Zq8Lm2Xv9Rt4Wk7Pd3Hs"
	q := bs + `"`
	for in, want := range map[string]string{
		`{"l": "{` + q + `api_key` + q + `: ` + q + v + q + `, ` + q + `n` + q + `: 1}"}`: `{"l": "{` + q + `api_key` + q + `: ` + q,
		"https://h/cb?x=1&api_key%3D" + v + "%26y%3D2":                                    "https://h/cb?x=1&api_key%3D",
		"Cookie: a=1; session=" + v + "; b=2":                                             "Cookie: a=1; session=",
		"tool --password " + v + " --verbose":                                             "tool --password ",
	} {
		out, err := hubRedactor().Text([]byte(in))
		require.NoError(t, err)
		assert.NotContains(t, string(out), v, in)
		assert.Contains(t, string(out), want, in)
	}
	// The tail after the value survives.
	out, err := hubRedactor().Text([]byte("tool --password " + v + " --verbose"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "--verbose")
	out, err = hubRedactor().Text([]byte("Cookie: a=1; session=" + v + "; b=2"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "; b=2")
}

// A value is found wherever it falls in a text longer than a scan chunk.
func TestKeywordRound2ValuesAreFoundAcrossChunkEdges(t *testing.T) {
	require.NoError(t, Init())
	v := "Zq8Lm2Xv9Rt4Wk7Pd3Hs"
	q := bs + `"`
	for name, in := range map[string]string{
		"json-in-json": q + `api_key` + q + `: ` + q + v + q,
		"encoded":      "next=https%3A%2F%2Fh%2Fcb%3Ftoken%3D" + v + "%26a%3D1",
		"cli":          "tool --password " + v + " run",
		"cookie":       "Cookie: a=1; session=" + v,
		"exact name":   "PGPASSWORD=" + v,
	} {
		filler := strings.Repeat("lorem ipsum dolor sit amet\n", 1)
		for _, off := range []int{0, 1, 7, 63, 64, 65, 4095, 4096, 4097, 8191, 8192, 8193, 65535, 65536, 65537} {
			text := strings.Repeat("x", off) + "\n" + in + "\n" + filler
			out, err := hubRedactor().Text([]byte(text))
			require.NoError(t, err)
			assert.NotContains(t, string(out), v, "%s at offset %d", name, off)
		}
	}
}
