package tunnel

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3732: a JSON reply held whole is redacted by its structure: every string, key or value, at
// any depth, is redacted as the text it stands for, and the reply is still the same JSON.

var jsonHdr = http.Header{"Content-Type": {"application/json"}}

// redactStrings is v with every string (and key) redacted as text: what the structural
// redaction must give.
func redactStrings(v any) any {
	switch x := v.(type) {
	case string:
		return string(RedactTokens([]byte(x)))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactStrings(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[string(RedactTokens([]byte(k)))] = redactStrings(e)
		}
		return out
	}
	return v
}

func decodeNumbers(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v), string(b))
	return v
}

// Every redactor fixture, as a value, as a key, nested, and as JSON inside a JSON string.
func TestRedactJSONStructureEveryFixture(t *testing.T) {
	var texts []string
	for _, tok := range []string{
		fake("ghp_", 36, alnum), fake("gho_", 40, alnum), fake("github_pat_", 30, alnum+"_"),
		fake("sk-ant-api03-", 40, alnum+"-_"), fake("sk-proj-", 30, alnum), fake("AKIA", 16, upper),
		fake("ASIA", 16, upper), fake("xoxb-", 30, alnum+"-"), fake("ghp_", 400, alnum),
	} {
		texts = append(texts, "see "+tok+" done")
	}
	for _, c := range authCases() {
		for _, text := range authForms("Authorization", c.value) {
			texts = append(texts, text)
		}
	}
	texts = append(texts, "cat id_rsa\n"+pemKey("RSA ", 4, "\n")+"\ndone")
	for _, text := range texts {
		inner, _ := json.Marshal(map[string]string{"content": text})
		for _, v := range []any{
			map[string]any{"content": text},
			map[string]any{text: 1},
			map[string]any{"messages": []any{map[string]any{"tool_uses": []any{map[string]any{"output": text}}}}, "total": 3},
			map[string]any{"content": string(inner)},
			[]any{text, 2, true, nil},
		} {
			raw, err := json.Marshal(v)
			require.NoError(t, err)
			out, ok := redactReply(jsonHdr, raw)
			require.True(t, ok)
			require.True(t, json.Valid(out), "%s", out)
			assert.Equal(t, redactStrings(decodeNumbers(t, raw)), decodeNumbers(t, out), "%.80s", raw)
		}
	}
}

// What isn't a string is kept as it was: numbers as written, key order, and characters
// json.Marshal would escape for HTML.
func TestRedactJSONStructureKeepsTheRest(t *testing.T) {
	raw := `{"z": 1e400, "a": 12345678901234567890, "m": [1.0, -0, true, false, null], "h": "<b>&amp;</b>", "u": "é😀\u2028"}`
	out, ok := redactReply(jsonHdr, []byte(raw))
	require.True(t, ok)
	assert.Equal(t, `{"z":1e400,"a":12345678901234567890,"m":[1.0,-0,true,false,null],"h":"<b>&amp;</b>","u":"é😀\u2028"}`, string(out))
}

// A reply that isn't JSON, or isn't valid, is redacted as text, as before.
func TestRedactReplyNotJSON(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	for _, c := range []struct {
		hdr  http.Header
		body string
	}{
		{http.Header{"Content-Type": {"text/plain"}}, `{"a":"` + tok + `"}`},
		{jsonHdr, `{"a":"` + tok + `"`},
		{jsonHdr, `{"a":1}{"b":"` + tok + `"}`},
	} {
		out, ok := redactReply(c.hdr, []byte(c.body))
		require.True(t, ok)
		assert.Equal(t, string(RedactTokens([]byte(c.body))), string(out))
		assert.NotContains(t, string(out), tok)
	}
}

// Security's property, for the structural path: any JSON built from the atoms, at any depth,
// is valid after redaction and is exactly its strings redacted.
func TestRedactJSONStructureProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	atoms := []string{"authorization", "Authorization: ", "\"", "\\", ": ", "Basic ", "Bearer ", "token ", "Digest ", "username=", "nonce=", "response=", "\"x\"", "=", ",", " ", "\n", "abc123", "é", "😀", "'", "{", "}", "[", "]", fake("ghp_", 36, alnum), "\\\""}
	str := func() string {
		var sb strings.Builder
		for i, n := 0, rng.Intn(16); i < n; i++ {
			sb.WriteString(atoms[rng.Intn(len(atoms))])
		}
		return sb.String()
	}
	var value func(depth int) any
	value = func(depth int) any {
		switch k := rng.Intn(6); {
		case depth > 3 || k < 2:
			return str()
		case k == 2:
			return rng.Intn(1000)
		case k == 3:
			a := make([]any, rng.Intn(4))
			for i := range a {
				a[i] = value(depth + 1)
			}
			return a
		default:
			m := map[string]any{}
			for i, n := 0, rng.Intn(4); i < n; i++ {
				m[str()] = value(depth + 1)
			}
			return m
		}
	}
	for it := 0; it < 20000; it++ {
		raw, err := json.Marshal(map[string]any{"content": value(0)})
		require.NoError(t, err)
		out, ok := redactReply(jsonHdr, raw)
		require.True(t, ok)
		if !json.Valid(out) {
			t.Fatalf("invalid: in=%s out=%s", raw, out)
		}
		require.Equal(t, redactStrings(decodeNumbers(t, raw)), decodeNumbers(t, out), "%s", raw)
	}
}
