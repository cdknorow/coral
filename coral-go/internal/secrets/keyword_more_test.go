package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round four (#5212): ten shapes, 2000 random values each, none may come back, as text and as a JSON reply; the name stays where there is one; what is not a secret is left alone.
func TestRoundFourShapesAreRedacted(t *testing.T) {
	require.NoError(t, Init())
	shapes := []struct {
		name   string
		format string // %s is the value
	}{
		{"url-encoded JSON pair", "GET /view?cfg=%%7B%%22api_key%%22%%3A%%22%s%%22%%7D"},
		{"url-encoded JSON pair, twice", "GET /view?cfg=%%257B%%2522token%%2522%%253A%%2522%s%%2522%%257D"},
		{"XML element", "<config><api_key>%s</api_key></config>"},
		{"XML element, prefixed name", "<x-api-key>\n  %s\n</x-api-key>"},
		{"HTML form field, name first", `<input type="hidden" name="api_key" value="%s">`},
		{"HTML form field, value first", `<input value="%s" name="secret">`},
		{"double-escaped quote entities", "&amp;quot;api_key&amp;quot;: &amp;quot;%s&amp;quot;"},
		{"triple-escaped quote entities", "&amp;amp;quot;token&amp;amp;quot;: &amp;amp;quot;%s&amp;amp;quot;"},
		{"quote entities without the semicolon", "&quotapi_key&quot: &quot%s&quot"},
		{"password after an encoded equals and space", "https://x.test/login?password%%3D%%20%s"},
		{"password after an encoded equals and plus", "https://x.test/login?password%%3D+%s"},
		{"curl -b glued on", "curl -bsession=%s https://api.test/x"},
		{"curl -b glued on, quoted", `curl -b"session=%s" https://api.test/x`},
		{"curl -b with a space and single quotes", "curl -s -b 'sid=%s; theme=dark' https://api.test/x"},
		{"curl --cookie=", "curl --cookie=session=%s https://api.test/x"},
		{"encoded underscore in the name", "GET /v1/items?api%%5Fkey=%s"},
		{"encoded hyphen in the name", "GET /v1/items?x%%2Dapi%%2Dkey=%s"},
		{"docker login -p", "docker login -u deploy -p %s registry.test"},
		{"docker login --password", "docker login --username deploy --password %s registry.test"},
		{"docker login --password=", "docker login --password=%s registry.test"},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			for i := 0; i < 2000; i++ {
				value := fake(32, alnumAZ, uint32(i*7919+len(s.name)))
				text := fmt.Sprintf(s.format, value)
				out, err := hubRedactor().Text([]byte(text))
				require.NoError(t, err)
				if strings.Contains(string(out), value) {
					t.Fatalf("%s: sample %d leaked: %.90q", s.name, i, text)
				}
				raw, _ := json.Marshal(map[string]any{"content": text})
				reply, err := hubRedactor().Reply(raw, true)
				require.NoError(t, err)
				if strings.Contains(string(reply), value) {
					t.Fatalf("%s: sample %d leaked in a JSON reply: %.90q", s.name, i, text)
				}
			}
		})
	}
}

// An encoded tail is not left showing: a value that ends in %5F (an encoded underscore) goes whole.
func TestAnEncodedTailOfTheValueGoesToo(t *testing.T) {
	require.NoError(t, Init())
	for _, text := range []string{"GET /x?api%5Fkey=AbCdEfGh12345678%5F", "&quot;token&quot;: &quotAbCdEfGh12345678%2D&quot", "<api_key>AbCdEfGh12345678%5F</api_key>"} {
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.NotContains(t, string(out), "AbCdEfGh", text)
		assert.True(t, strings.HasSuffix(string(out), "[redacted token]") || strings.Contains(string(out), "[redacted token]"), "the marker stands for the whole value: %q", string(out))
		assert.NotRegexp(t, `\[redacted token\](?:5F|2D|%5F|%2D)`, string(out), "nothing of the encoded tail is left after the marker")
	}
}

// What is not a secret stays: names without a value, short values, placeholders, other element and field names, docker without a password, a curl cookie file, --password-stdin.
func TestRoundFourLeavesOrdinaryTextAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, text := range []string{
		"<api_key></api_key>",
		"<api_key>${API_KEY}</api_key>",
		"<api_key>short</api_key>",
		"<title>Reset your password in settings</title>",
		`<input name="username" value="alice-the-user-name">`,
		`<input type="submit" value="Reset password">`,
		"<input name=\"api_key\" value=\"\">",
		"&quot;api_key&quot;: &quot;<your key>&quot;",
		"GET /v1/items?apikey2=abcdefghijklmnop",
		"docker login registry.test",
		"echo $PASS | docker login --password-stdin -u deploy registry.test",
		"curl -b cookies.txt https://api.test/x",
		"curl -b theme=dark https://api.test/x",
		"a & b &amp; c &quot; d",
		"100%25 sure about 50%5 of it",
	} {
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		assert.Equal(t, text, string(out), "left as it is")
	}
}

// Ordinary text does not change: the repository's own documents and sources (tests and fixtures aside, which hold secrets on purpose) have nothing for the round-four pass to find.
func TestRoundFourFindsNothingInTheRepositorysOwnText(t *testing.T) {
	var found []string
	n := 0
	_ = filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "vendor", "testdata", "hubharness":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go", ".js", ".sh", ".html", ".yml", ".json", ".txt", ".css":
		default:
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || info.Size() > 2<<20 || strings.Contains(filepath.ToSlash(path), "internal/secrets/keyword_") {
			return nil // the rules' own source documents the commands they take (docker login -p VALUE)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		n++
		text := string(b)
		old := keywordSpans(text)
		for _, sp := range moreKeywordSpans(text) {
			covered := false
			for _, o := range old {
				if o.Start <= sp.Start && sp.End <= o.End {
					covered = true
					break
				}
			}
			if !covered { // new coverage only: what the keyword rules already take is not round four's
				found = append(found, fmt.Sprintf("%s: %.40q", filepath.ToSlash(path), text[sp.Start:min(sp.End, sp.Start+12)]))
			}
		}
		return nil
	})
	require.Greater(t, n, 500, "the walk found the repository's files")
	assert.Empty(t, found, "the round-four pass finds a value in ordinary text")
}

func FuzzMoreKeywordSpans(f *testing.F) {
	for _, s := range []string{"<api_key>abcdefgh1234</api_key>", `<input name="token" value="abcdefgh1234">`, "&amp;quot;api_key&amp;quot;: &amp;quot;abcdefgh1234&amp;quot;", "api%5Fkey=abcdefgh1234%5F", "curl -b\"s=abcdefgh1234\"", "docker login -p abcdefgh", "%22token%22%3A%22abcdefgh1234%22", "&quot&quot&quot", "%5F%5F%2D"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		spans := moreKeywordSpans(text)
		for _, sp := range spans {
			require.True(t, sp.Start >= 0 && sp.Start < sp.End && sp.End <= len(text), "span %v in %d bytes", sp, len(text))
		}
		require.NoError(t, Init())
		out, err := hubRedactor().Text([]byte(text))
		require.NoError(t, err)
		_ = out
	})
}

// The round-four pass is linear (the lesson of #5018): shapes that would make a backtracking matcher or a copy per entity blow up, at a size and four times it.
func TestRoundFourIsLinearOnAdversarialText(t *testing.T) {
	require.NoError(t, Init())
	shapes := map[string]func(n int) string{
		"entities":              func(n int) string { return strings.Repeat("&amp;", n/5) + "quot;" },
		"entity quotes":         func(n int) string { return strings.Repeat("&quot", n/5) },
		"encoded names":         func(n int) string { return strings.Repeat("%5F%2D", n/6) },
		"open input tags":       func(n int) string { return strings.Repeat("<input name=\"api_key\" ", n/22) },
		"input, never closed":   func(n int) string { return "<input " + strings.Repeat("name=x ", n/7) },
		"open elements":         func(n int) string { return strings.Repeat("<api_key>", n/9) },
		"encoded quote pairs":   func(n int) string { return strings.Repeat("%22token%22%3A%22", n/17) },
		"curl -b repeated":      func(n int) string { return strings.Repeat("curl -b ", n/8) },
		"curl, one long line":   func(n int) string { return "curl " + strings.Repeat("x ", n/2) + "-b a=" },
		"docker login repeated": func(n int) string { return strings.Repeat("docker login ", n/13) },
		"docker, one long line": func(n int) string { return "docker " + strings.Repeat("x ", n/2) + "login -p" },
		"cookie pairs":          func(n int) string { return "curl -b " + strings.Repeat("a=b;", n/4) },
	}
	timeIt := func(text string) time.Duration {
		best := time.Hour
		for i := 0; i < 3; i++ {
			start := time.Now()
			moreKeywordSpans(text)
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
			if dl > 150*time.Millisecond {
				require.Less(t, float64(dl)/float64(max(ds, 5*time.Millisecond)), 9.0, "%s: %v for %d bytes, %v for %d", name, ds, small, dl, large)
			}
			require.Less(t, dl, 10*time.Second, "%s: %v for %d bytes", name, dl, large)
			start := time.Now()
			_, err := hubRedactor().Text([]byte(mk(small)))
			require.NoError(t, err, "within the redactor's budget")
			require.Less(t, time.Since(start), Budget, name)
		})
	}
}

// Ordinary text costs a handful of Contains calls: source-like text with none of the triggers is scanned in about the time of one pass over it.
func TestRoundFourIsCheapOnOrdinaryText(t *testing.T) {
	text := strings.Repeat("func (s *Store) ListTasks(ctx context.Context, project string) ([]Task, error) {\n\treturn s.query(ctx, project)\n}\n", 8000)
	start := time.Now()
	for i := 0; i < 20; i++ {
		_ = moreKeywordSpans(text)
	}
	per := time.Since(start) / 20
	assert.Less(t, per, 30*time.Millisecond, "%d bytes of ordinary text: %v a pass", len(text), per)
}
