package secrets

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5172: Security's two leaks of the round-2 rules: a URL-encoded colon with its space (password%3A%20VALUE) and a value quoted with HTML entities
// (&quot;api_key&quot;: &quot;VALUE&quot;). Same bar: 2000 seeded samples per shape, zero survivors, the gate equal to the rules, prose and placeholders left.

func shapes3() []shape {
	tok := valueShape{alnumAZ, 20, 64}
	pw := valueShape{alnumAZ, 10, 30}
	hex32 := valueShape{hexAlpha, 32, 32}
	var out []shape
	add := func(name string, val valueShape, text func(string) string) { out = append(out, shape{name, text, val}) }
	// A URL-encoded colon, with its space encoded or as a plus, or alone; encoded twice.
	add("password%3A%20", pw, func(v string) string { return "form=user%3Dbob%26password%3A%20" + v + "%26ok%3D1" })
	add("password%3A%20 at the end", pw, func(v string) string { return "q=password%3A%20" + v })
	add("api_key%3A%20", tok, func(v string) string { return "q=api_key%3A%20" + v + "&x=1" })
	add("token%3A+", tok, func(v string) string { return "q=token%3A+" + v })
	add("secret%3A", tok, func(v string) string { return "q=client_secret%3A" + v })
	add("password%253A%2520 (twice)", pw, func(v string) string { return "q=password%253A%2520" + v })
	add("PGPASSWORD%3A%20", pw, func(v string) string { return "env=PGPASSWORD%3A%20" + v })
	add("nested redirect, token%3A%20", tok, func(v string) string {
		return "https://a.example.com/login?next=https%3A%2F%2Fb.example.com%2Fcb%3Ftoken%3A%20" + v + "%26a%3D1"
	})
	// HTML entities for the quotes.
	add("&quot; api_key", tok, func(v string) string { return `{&quot;api_key&quot;: &quot;` + v + `&quot;}` })
	add("&quot; password", pw, func(v string) string { return `{&quot;password&quot;: &quot;` + v + `&quot;, &quot;n&quot;: 1}` })
	add("&quot; token, no space", tok, func(v string) string { return `&quot;token&quot;:&quot;` + v + `&quot;` })
	add("&quot; client_secret hex", hex32, func(v string) string { return `&quot;client_secret&quot;: &quot;` + v + `&quot;` })
	add("&#34; token", tok, func(v string) string { return `&#34;token&#34;: &#34;` + v + `&#34;` })
	add("&#x22; secret", tok, func(v string) string { return `&#x22;secret&#x22;: &#x22;` + v + `&#x22;` })
	add("&#x0022; api_key (padded)", tok, func(v string) string { return `&#x0022;api_key&#x0022;: &#x0022;` + v + `&#x0022;` })
	add("&apos; api_key =", tok, func(v string) string { return `&apos;api_key&apos; = &apos;` + v + `&apos;` })
	add("&#39; password", pw, func(v string) string { return `&#39;password&#39;: &#39;` + v + `&#39;` })
	add("entities in a log line", tok, func(v string) string { return `<pre>config = {&quot;auth_token&quot;: &quot;` + v + `&quot;}</pre>` })
	return out
}

func TestKeywordRound3ValuesAreRedacted(t *testing.T) {
	require.NoError(t, Init())
	for _, sh := range shapes3() {
		t.Run(sh.name, func(t *testing.T) {
			r := rand.New(rand.NewSource(5172))
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

// The gate finds them too: the gated spans equal the rules' over the whole text.
func TestKeywordGateFindsRound3Shapes(t *testing.T) {
	for _, sh := range shapes3() {
		r := rand.New(rand.NewSource(5172))
		for i := 0; i < 100; i++ {
			text := sh.text(sh.val.make(r))
			sameCoverage(t, sh.name, text)
			sameCoverage(t, sh.name+" in lines", "pre\n"+text+"\npost: 1\n")
		}
	}
}

func TestKeywordRound3LeavesProseAndPlaceholdersAlone(t *testing.T) {
	for _, text := range []string{
		"password%3A%20", "password%3A", "q=password%3A%20short", "q=token%3A%20abc",
		`&quot;password&quot;: &quot;&quot;`, `&quot;api_key&quot;: &quot;$KEY&quot;`, `&quot;token&quot;: &quot;<token>&quot;`, `&quot;api_key&quot;: &quot;${KEY}&quot;`,
		`a &quot;token&quot; is a word`, `&quot;mytoken&quot;: &quot;abcdefghijklmnop1234&quot;`, `&quot;tokens&quot;: 5`,
		`&quot;password&quot;: &quot;short&quot;`, `&#34;token&#34;: &#34;abc&#34;`, `&amp;password=short`,
		"mytoken%3A%20abcdefghijklmnop1234", "tokens%3A%205",
	} {
		assert.Empty(t, keywordSpans(text), "%q", text)
	}
}
