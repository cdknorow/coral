package secrets

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FuzzKeywordValues: whatever precedes a keyword and follows its value, a value of 16 or more letters and digits after "api_key=" never comes back,
// and what the rules find is always inside the text (#5132).
func FuzzKeywordValues(f *testing.F) {
	f.Add([]byte("0123456789abcdef"), "GET /x?a=1\n", "&limit=5")
	f.Add([]byte("AAAAAAAAAAAAAAAAAAAAAAAA"), "", " tail")
	f.Add([]byte{1, 2, 3, 250, 251, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, "export X=1; ", "\"")
	f.Add([]byte("zzzzzzzzzzzzzzzzzzzz"), "{\"a\": 1}\n", ",\n")
	f.Fuzz(func(t *testing.T, seed []byte, prefix, suffix string) {
		if len(seed) == 0 || len(seed) > 4096 {
			return
		}
		value := make([]byte, 0, len(seed)+16)
		for _, b := range seed {
			value = append(value, alnumAZ[int(b)%len(alnumAZ)])
		}
		for len(value) < 16 {
			value = append(value, value...)
		}
		if placeholderValue(string(value)) {
			return // one repeated character is a placeholder by design (the rules' comment)
		}
		// the value ends at what follows: a space, a quote, '&' or the end; and the keyword stands alone (after a line end)
		if suffix != "" && !strings.ContainsAny(suffix[:1], " \n\t\"'&;,<>)]}") {
			suffix = " " + suffix
		}
		text := prefix + "\napi_key=" + string(value) + suffix
		require.NoError(t, Init())
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		require.NotContains(t, string(out), string(value))
		for _, sp := range keywordSpans(text) {
			require.True(t, sp.Start >= 0 && sp.Start < sp.End && sp.End <= len(text), "span %v in %d bytes", sp, len(text))
		}
	})
}

// Adversarial text (#5018's lesson: the redactor is linear in what it is given): the shapes that would make a backtracking matcher blow up, at a size,
// then four times the size, in the keyword rules alone: the time grows about four times, not sixteen, and the whole redactor finishes inside its budget.
func TestKeywordRulesAreLinearOnAdversarialText(t *testing.T) {
	require.NoError(t, Init())
	shapes := map[string]func(n int) string{
		"a name chain":             func(n int) string { return strings.Repeat("a_", n/2) + "token=" },
		"a name chain with dashes": func(n int) string { return strings.Repeat("ab-", n/3) + "x-api-key: " },
		"many keywords, no value":  func(n int) string { return strings.Repeat("token=\n", n/7) },
		"many short values":        func(n int) string { return strings.Repeat("api_key=abc ", n/12) },
		"one value, very long":     func(n int) string { return "api_key=" + strings.Repeat("A1", n/2) },
		"curl, no -u":              func(n int) string { return strings.Repeat("curl ", n/5) },
		"curl -u, long line":       func(n int) string { return "curl " + strings.Repeat("x ", n/2) + "-u a:" },
		"exports":                  func(n int) string { return strings.Repeat("export ", n/7) + "A_KEY=" },
		"query separators":         func(n int) string { return strings.Repeat("?&;", n/3) + "key=" },
		"upper env names":          func(n int) string { return strings.Repeat("AB_", n/3) + "KEY=" },
		"quotes and colons":        func(n int) string { return strings.Repeat(`":'=`, n/4) },
		"escaped quotes":           func(n int) string { return strings.Repeat(`\"api_key\": \"`, n/16) },
		"encoded names":            func(n int) string { return strings.Repeat("%3F%26a%3Db", n/12) + "token%3D" },
		"encoded value, long":      func(n int) string { return "api_key%3D" + strings.Repeat("A%2B", n/3) },
		"cli flags":                func(n int) string { return strings.Repeat("--password ", n/11) },
		"cli flag chain":           func(n int) string { return strings.Repeat("--a-", n/4) + "password x" },
		"mysql, no -p":             func(n int) string { return strings.Repeat("mysql ", n/6) },
		"cookie header, many":      func(n int) string { return strings.Repeat("Cookie: a=b; ", n/13) },
		"cookie pairs, one line":   func(n int) string { return "Cookie: " + strings.Repeat("a=b; ", n/5) },
		"full-width":               func(n int) string { return strings.Repeat("token\uFF1D\uFF1A", n/8) },
		"authorization repeated":   func(n int) string { return strings.Repeat("authorization: bearer ", n/22) },
	}
	timeIt := func(text string) time.Duration {
		best := time.Hour
		for i := 0; i < 3; i++ {
			start := time.Now()
			keywordSpans(text)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	const small, large = 250_000, 1_000_000
	for name, mk := range shapes {
		t.Run(name, func(t *testing.T) {
			ds, dl := timeIt(mk(small)), timeIt(mk(large))
			// linear is about 4; quadratic would be about 16. A generous floor keeps a fast small run from making the ratio noise.
			if dl > 150*time.Millisecond {
				require.Less(t, float64(dl)/float64(max(ds, 5*time.Millisecond)), 9.0, "%s: %v for %d bytes, %v for %d", name, ds, small, dl, large)
			}
			// the floor only catches a blow-up: a shared CI runner is several times slower than a laptop
			require.Less(t, dl, 10*time.Second, "%s: %v for %d bytes", name, dl, large)
			start := time.Now()
			_, err := hubRedactor().Text([]byte(mk(small)))
			require.NoError(t, err, "within the redactor's budget")
			require.Less(t, time.Since(start), Budget, name)
		})
	}
}
