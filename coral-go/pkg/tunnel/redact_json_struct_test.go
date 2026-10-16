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

// redactStrings is v with every key redacted as text and every value with its key (an array's
// strings with the key of the member holding it): what the structural redaction must give.
func redactStrings(v any) any { return redactUnder(v, "", false, false, false) }

func redactUnder(v any, key string, has, auth, secret bool) any {
	if secret { // under a secret-named key (#3733): every value but "" and null is the marker
		switch x := v.(type) {
		case string:
			if x == "" {
				return x
			}
			return RedactMarker
		case nil:
			return nil
		case bool, float64, json.Number:
			return RedactMarker
		}
	}
	switch x := v.(type) {
	case string:
		switch {
		case auth:
			return redactMember("Authorization", x)
		case has:
			return redactMember(key, x)
		}
		return string(RedactTokens([]byte(x)))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactUnder(e, key, has, auth, secret)
		}
		return out
	case map[string]any:
		name, hasName := "", false
		for k, e := range x {
			if lk := strings.ToLower(k); lk == "name" || lk == "key" {
				if str, isStr := e.(string); isStr {
					name, hasName = str, true
				}
			}
		}
		mentions := 0
		for _, e := range x {
			if str, isStr := e.(string); isStr && namesAuthorization(str) {
				mentions++
			}
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			ctx := k
			if hasName && strings.EqualFold(k, "value") {
				ctx = name
			}
			str, isStr := e.(string)
			other := mentions
			if isStr && namesAuthorization(str) {
				other--
			}
			childAuth := auth || strings.Contains(strings.ToLower(k), "authorization") || other > 0 && isStr
			out[string(RedactTokens([]byte(k)))] = redactUnder(e, ctx, true, childAuth, secret || isSecretKey(ctx))
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

// Security's rows (#3732 review): in a JSON object the keyword is the key and the credential
// its value. None of the secret shows, structurally; nor through the text redactor on the same
// bytes, except where noted.
func TestRedactJSONStructureKeyAndValue(t *testing.T) {
	basic := fake("", 24, alnum+"=")
	tok := fake("", 36, alnum)
	bearer := fake("", 30, alnum+"._-")
	nonce := fake("", 32, "0123456789abcdef")
	resp := fake("", 32, "fedcba9876543210")
	ghp := fake("ghp_", 36, alnum)
	digest := `Digest username="bob", nonce="` + nonce + `", uri="/v1", response="` + resp + `"`
	for _, c := range []struct {
		body    any
		secrets []string
		textGap bool // the text redactor misses it (a value in an array, not after a quote); held JSON goes the structural way
	}{
		{map[string]any{"headers": map[string]any{"Authorization": "Basic " + basic}}, []string{basic}, false},
		{map[string]any{"authorization": "Basic " + basic}, []string{basic}, false},
		{map[string]any{"AUTHORIZATION": "Basic " + basic}, []string{basic}, false},
		{map[string]any{"Authorization": "token " + tok}, []string{tok}, false},
		{map[string]any{"Authorization": tok}, []string{tok}, false},
		{map[string]any{"Proxy-Authorization": "Basic " + basic}, []string{basic}, false},
		{map[string]any{"Authorization": digest}, []string{nonce, resp}, false},
		{map[string]any{"Authorization": "Bearer " + bearer}, []string{bearer}, false},
		{map[string]any{"token": ghp}, []string{ghp[4:]}, false},
		{map[string]any{"Authorization": []any{"Basic " + basic}}, []string{basic}, true},
		{map[string]any{"req": map[string]any{"header": map[string]any{"Authorization": []any{"Bearer " + bearer, "Basic " + basic}}}}, []string{bearer, basic}, true},
		{[]any{map[string]any{"name": "Authorization", "value": "Basic " + basic}}, []string{basic}, true},
		{[]any{map[string]any{"value": "Basic " + basic, "Name": "Proxy-Authorization"}}, []string{basic}, true},
		// Security's SHOULD rows: the keyword is an object's key, or another member names it.
		{map[string]any{"Authorization": map[string]any{"scheme": "Basic", "credentials": basic}}, []string{basic}, true},
		{map[string]any{"items": []any{map[string]any{"k": "Authorization", "v": "Basic " + basic}}}, []string{basic}, true},
		{map[string]any{"auth": map[string]any{"proxyAuthorization": map[string]any{"value": map[string]any{"token": tok}}}}, []string{tok}, true},
		{map[string]any{"env": []any{map[string]any{"n": "HTTP_AUTHORIZATION", "v": "Bearer " + bearer}}}, []string{bearer}, true},
		{map[string]any{"header": "Proxy-Authorization:", "credential": "Basic " + basic}, []string{basic}, true},
		{map[string]any{"field": "authorization ", "data": tok, "n": 3}, []string{tok}, true},
	} {
		raw, err := json.Marshal(c.body)
		require.NoError(t, err)
		out, ok := redactReply(jsonHdr, raw)
		require.True(t, ok)
		require.True(t, json.Valid(out), "%s", out)
		assert.Empty(t, authSecretLeak(string(out), c.secrets), "structural: %s -> %s", raw, out)
		if text := string(RedactTokens(raw)); !c.textGap {
			assert.Empty(t, authSecretLeak(text, c.secrets), "text: %s -> %s", raw, text)
		}
	}
}

// A reply nested deeper than maxJSONDepth isn't walked: it is redacted as text, not refused.
func TestRedactJSONTooDeepFallsBackToText(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	raw := strings.Repeat("[", maxJSONDepth+5) + `"` + tok + `"` + strings.Repeat("]", maxJSONDepth+5)
	out, ok := redactReply(jsonHdr, []byte(raw))
	require.True(t, ok)
	assert.True(t, json.Valid(out))
	assert.NotContains(t, string(out), tok)
}

// Near misses for the Authorization inheritance: an object that only mentions authorization in
// passing, and a key that only resembles it, are redacted as before.
func TestRedactJSONStructureAuthorizationNearMisses(t *testing.T) {
	for _, raw := range []string{
		`{"note":"authorization was refused","detail":"see the log"}`,
		`{"authorize":"later","msg":"retry in a minute"}`,
		`{"kind":"authorization_request","text":"hello world"}`,
	} {
		out, ok := redactReply(jsonHdr, []byte(raw))
		require.True(t, ok)
		assert.Equal(t, raw, string(out))
	}
}

// A key that is itself a token: redaction changes the key, so where its value starts in "key:
// value" is unknown, and the value is replaced with the marker (fail closed; QA on #3732).
func TestRedactJSONStructureTokenKeyFailsClosed(t *testing.T) {
	key := fake("ghp_", 36, alnum)
	secret := "Basic " + fake("", 24, alnum+"=")
	raw, err := json.Marshal(map[string]any{key: secret, "other": "kept as it is"})
	require.NoError(t, err)
	out, ok := redactReply(jsonHdr, raw)
	require.True(t, ok)
	var got map[string]string
	require.NoError(t, json.Unmarshal(out, &got), string(out))
	assert.Equal(t, map[string]string{RedactMarker: RedactMarker, "other": "kept as it is"}, got)
	assert.Equal(t, RedactMarker, redactMember(key, "anything at all"))
}
