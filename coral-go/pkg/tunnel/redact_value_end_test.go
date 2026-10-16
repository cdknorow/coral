package tunnel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3761 (found by FuzzRedactStream, in #3733's text rule): a value never takes another rule's
// keyword with it. In "token:  Authorization: Basic <b64>" the secret-name rule took
// "Authorization:" as token's value and redacted it, so the Authorization rule never saw its
// keyword and the Basic credential went out; "token: DB_PASSWORD=\"<pw>\"" let the password out the
// same way. Raw, in JSON and in JSON inside JSON, at every split into two writes.
func TestRedactValueEndsBeforeAnotherRule(t *testing.T) {
	b64 := fake("", 24, alnum+"=")
	pw := fake("", 30, alnum)
	hex := fake("", 32, "0123456789abcdef")
	for _, c := range []struct{ text, secret string }{
		{"token:  Authorization: Basic " + b64, b64},
		{"token: Authorization: Bearer " + pw, pw},
		{`token: DB_PASSWORD="` + pw + `"`, pw},
		{"password=api_key=" + pw, pw},
		{`secret: Authorization: Digest username="bob", nonce="` + hex + `"`, hex},
		{"token=postgres://app:" + pw + "@db/main", pw},
		{"Bearer password=" + pw, pw},
	} {
		js, _ := json.Marshal(map[string]string{"content": c.text})
		jsjs, _ := json.Marshal(map[string]string{"content": string(js)})
		for enc, body := range map[string][]byte{"raw": []byte(c.text), "json": js, "json-in-json": jsjs} {
			whole := string(RedactTokens(body))
			assert.Empty(t, urlSecretShown(whole, c.secret), "%s (%s): %s", c.text, enc, whole)
			if enc != "raw" {
				assert.True(t, json.Valid([]byte(whole)), "%s (%s) stays JSON: %s", c.text, enc, whole)
			}
			for k := 0; k <= len(body); k++ {
				if got := urlRedactTwoWrites(t, body, k); got != whole {
					t.Errorf("%s (%s) split at %d: %q, whole %q", c.text, enc, k, got, whole)
					break
				}
			}
		}
	}
	// A keyword the next rule doesn't take (no value after it) is not a start: the value runs on.
	out := string(RedactTokens([]byte("token=" + pw + "token rest")))
	assert.False(t, strings.Contains(out, pw[:8]), out)
}
