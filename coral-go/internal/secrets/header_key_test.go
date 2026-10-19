package secrets

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5190: betterleaks' generic rule drops a value that holds a common word, so "X-Api-Key: <32 characters with 'house' in them>" was shown whole (200 of 200). The header's name says what the value is.
var headerNames = []string{"X-Api-Key", "x-api-key", "Api-Key", "apikey", "X-Auth-Token", "X-Access-Token", "X-Api-Token", "X-Secret-Key", "X-Auth-Key", "Private-Token"}
var commonWords = []string{"false", "house", "table", "apple", "water", "token", "client", "server", "dashboard", "password", "random", "build"}

func TestAHeaderNamedKeyHoldingAWordIsRedacted(t *testing.T) {
	require.NoError(t, Init())
	r := rand.New(rand.NewSource(5190))
	frames := []struct{ name, format string }{
		{"a header line", "%s: %s"},
		{"a curl header", `curl -H '%s: %s' https://api.example.test/v1`},
		{"a JSON header map", `{"headers": {"%s": "%s"}}`},
		{"in a request dump", "GET /v1/things HTTP/1.1\nHost: api.example.test\n%s: %s\nAccept: */*\n"},
	}
	for _, name := range headerNames {
		for i := 0; i < 200; i++ {
			body := make([]byte, 32)
			for k := range body {
				body[k] = alnumAZ[r.Intn(len(alnumAZ))]
			}
			word := ""
			if i%4 != 0 { // a quarter are plain random keys, the rest hold a word
				word = commonWords[r.Intn(len(commonWords))]
				if i%3 == 0 {
					word = strings.ToUpper(word[:1]) + word[1:]
				}
				at := r.Intn(len(body) - len(word))
				copy(body[at:], word)
			}
			key := string(body)
			f := frames[i%len(frames)]
			text := fmt.Sprintf(f.format, name, key)
			out, err := Redactor().Text([]byte(text))
			require.NoError(t, err)
			assert.NotContains(t, string(out), key, "%s, %s, word %q: the key is redacted", name, f.name, word)
			assert.Contains(t, string(out), name, "the header's name stays")
		}
	}
}

// What is not a header key stays: prose, short values, placeholders and other names are not redacted by this rule.
func TestHeaderKeyRuleLeavesOrdinaryTextAlone(t *testing.T) {
	require.NoError(t, Init())
	for _, text := range []string{
		"X-Api-Key: ${API_KEY}",
		"X-Api-Key: <your key here>",
		"X-Api-Key: short",
		"Send it in the X-Api-Key header, see the docs",
		"X-Request-Id: 8f2c1a77-3b40-4d2e-9f2b-0a1c5d6e7f80",
		"Content-Type: application/json; charset=utf-8",
		"the api key: is described below in a long enough sentence",
	} {
		out, err := Redactor().Text([]byte(text))
		require.NoError(t, err)
		assert.Equal(t, text, string(out), "left as it is")
	}
}

// Ordinary text does not change: the repository's own documents and sources have no header key but two test secrets, so the rule finds nothing else in them.
func TestHeaderKeyRuleFindsNothingInTheRepositorysOwnText(t *testing.T) {
	var found []string
	root := filepath.Join("..", "..")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && (info.Name() == "node_modules" || info.Name() == ".git" || info.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go", ".js", ".sh", ".html", ".yml", ".json", ".txt":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 2<<20 {
			return nil
		}
		for _, m := range extraRules[headerKeyRuleIndex()].re.FindAllStringSubmatch(string(b), -1) {
			found = append(found, filepath.ToSlash(path)+": "+m[0])
		}
		return nil
	})
	for _, f := range found {
		assert.True(t, strings.Contains(f, "page_secrets_test.go") || strings.Contains(f, "header_key_test.go"), "an unexpected header key in ordinary text: %s", f)
	}
}

// headerKeyRuleIndex is the header rule's place in extraRules (found by what it matches, so a reordering does not break the test).
func headerKeyRuleIndex() int {
	for i, r := range extraRules {
		if r.re.MatchString("X-Api-Key: abcdefghijklmnop1234") {
			return i
		}
	}
	return -1
}
