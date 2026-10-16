package tunnel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3731: an Authorization value is redacted however it is written and however it arrives. A
// quoted "authorization": "Basic ..." was redacted in raw text and passed once inside a JSON
// string (its quotes are \" there), which is how Coral Hub sees chat and history replies.
// Every scheme (Basic, Bearer, token, Digest, none) x keyword case x quoting x raw, JSON and
// JSON inside JSON (an escaped backslash before the quote) x every split into two writes.

// authSecretLeak is the longest run (4 bytes or more) of any secret found in out, or "".
func authSecretLeak(out string, secrets []string) string {
	for _, secret := range secrets {
		for n := len(secret); n >= 4; n-- {
			for i := 0; i+n <= len(secret); i++ {
				if strings.Contains(out, secret[i:i+n]) {
					return secret[i : i+n]
				}
			}
		}
	}
	return ""
}

// redactTwoWrites runs body through one Redactor in two writes, split at k.
func redactTwoWrites(t *testing.T, body []byte, k int) string {
	t.Helper()
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, err := r.Write(body[:k])
	require.NoError(t, err)
	_, err = r.Write(body[k:])
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return out.String()
}

type authCase struct {
	name    string
	value   string   // what follows the keyword's separator
	secrets []string // what must never show
}

func authCases() []authCase {
	basic := fake("", 24, alnum+"=")
	bearer := fake("", 40, alnum+"._-")
	tok := fake("", 30, alnum)
	nonce := fake("", 32, "0123456789abcdef")
	resp := fake("", 32, "fedcba9876543210")
	bare := fake("", 30, alnum)
	return []authCase{
		{"Basic", "Basic " + basic, []string{basic}},
		{"Bearer", "Bearer " + bearer, []string{bearer}},
		{"token", "token " + tok, []string{tok}},
		{"Digest", `Digest username="bob", realm="api", nonce="` + nonce + `", uri="/v1", response="` + resp + `"`, []string{nonce, resp}},
		{"bare", bare, []string{bare}},
	}
}

// authForms writes a header with keyword kw and value v: as a header line, a JSON field, a
// single-quoted field, a key=value and a curl argument.
func authForms(kw, v string) map[string]string {
	return map[string]string{
		"header": "> " + kw + ": " + v + "\n< 200",
		"json":   `{"` + kw + `": "` + v + `", "accept": "*/*"}`,
		"single": `{'` + kw + `': '` + v + `'}`,
		"equals": kw + "=" + v + " next",
		"curl":   "curl -H '" + kw + ": " + v + "' https://example.test",
	}
}

func TestRedactAuthorizationEveryShapeEverySplit(t *testing.T) {
	for _, c := range authCases() {
		for _, kw := range []string{"Authorization", "authorization", "AUTHORIZATION"} {
			for form, text := range authForms(kw, c.value) {
				js, _ := json.Marshal(map[string]string{"content": text})
				jsjs, _ := json.Marshal(map[string]string{"content": string(js)})
				for enc, body := range map[string][]byte{"raw": []byte(text), "json": js, "json-in-json": jsjs} {
					whole := string(RedactTokens(body))
					if leak := authSecretLeak(whole, c.secrets); leak != "" {
						t.Errorf("%s %s %s %s: %q shows in %q", c.name, kw, form, enc, leak, whole)
						continue
					}
					if enc != "raw" {
						var v map[string]string
						assert.NoError(t, json.Unmarshal([]byte(whole), &v), "%s %s %s %s: still JSON: %s", c.name, kw, form, enc, whole)
					}
					for k := 0; k <= len(body); k++ {
						if got := redactTwoWrites(t, body, k); got != whole {
							t.Errorf("%s %s %s %s split at %d: %q, whole %q", c.name, kw, form, enc, k, got, whole)
							break
						}
					}
				}
			}
		}
	}
}

// A quote written with backslashes or as " is still a quote; a word after "authorization"
// that isn't a separator is not a header.
func TestQuoteLen(t *testing.T) {
	for in, want := range map[string]int{
		"\"x": 1, "'x": 1, "\\\"x": 2, "\\\\\\\"x": 4, "\\u0022x": 6, "\\U0027x": 6,
		"x": 0, "\\nx": 0, "\\u0041x": 0, "": 0,
		"\\": -2, "\\\\\\": -2, "\\u00": -2,
	} {
		assert.Equal(t, want, quoteLen([]byte(in)), in)
	}
}
