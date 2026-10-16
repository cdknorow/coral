package tunnel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3733 (Security on #3731): a value named as a secret is redacted, not only a value shaped like
// a token: by key in a JSON reply held whole, and as name=value or name: value in text and
// streams. Ordinary words that only start like a name are left.

const sm = RedactMarker

// In text (a streamed reply, a terminal, a text reply), raw and as JSON writes it.
func TestRedactSecretNamesInText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"password=hunter2", "password=" + sm},
		{"DB_PASSWORD=hunter", "DB_PASSWORD=" + sm}, // KEY=value: any value
		{"export GITHUB_TOKEN=abcdef next", "export GITHUB_TOKEN=" + sm + " next"},
		{"client_secret=abc&x=1", "client_secret=" + sm + "&x=1"},
		{"x-auth-token: s3cr3t", "x-auth-token: " + sm},
		{`{"api_key": "abc"}`, `{"api_key": "` + sm + `"}`},
		{`{"apiKey":"abc","n":1}`, `{"apiKey":"` + sm + `","n":1}`},
		{`{"PASSWD": 'pw'}`, `{"PASSWD": '` + sm + `'}`},
		{`accessToken: "q"`, `accessToken: "` + sm + `"`},
		{`PrivateKey='k'`, `PrivateKey='` + sm + `'`},
		{`Secret: abc123`, `Secret: ` + sm}, // bare after ':': letters and digits
		{`{"refresh_token": "r1", "token": "t2"}`, `{"refresh_token": "` + sm + `", "token": "` + sm + `"}`},
	} {
		assert.Equal(t, c.want, string(RedactTokens([]byte(c.in))), c.in)
		// The same text inside a JSON string: the quotes are \", and the result stays JSON.
		enc, _ := json.Marshal(map[string]string{"content": c.in})
		out := RedactTokens(enc)
		require.True(t, json.Valid(out), "%s -> %s", enc, out)
		var got map[string]string
		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, c.want, got["content"], "in JSON: %s", enc)
	}
}

func TestRedactSecretNamesNearMisses(t *testing.T) {
	for _, s := range []string{
		"tokens: 5", "max_tokens=5", `{"input_tokens": 12, "total_token_usage": "x"}`,
		"secretary: Bob", "the secret is out", "author: me", "auth: abc123", "mytoken=abc", "passwords=a1",
		`{"token": 5, "password": null, "secret": true, "api_key": ""}`, // not credentials (JSON numbers, null, true, empty)
		"token: expired", "password: please", // prose: a bare word after ':'
		"Authorization: unavailable",
	} {
		assert.Equal(t, s, string(RedactTokens([]byte(s))), "left as it is: %s", s)
	}
}

// A stream cut anywhere gives what the whole text gives.
func TestRedactSecretNamesEverySplit(t *testing.T) {
	for _, s := range []string{
		"DB_PASSWORD=hunter next", `{"api_key": "abc", "x": "Authorization: Basic dkryFMT07dkryFMT07dkryFM"}`,
		"x-auth-token: s3cr3t\n", "client_secret=abc&x=1", "tokens: 5, Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
		`{"content":"{\"apiKey\": \"abcd\", \"password\" : 'pw1'}"}`, // JSON inside JSON: a cut inside \"
		`{"content":"x\u0022token\u0022: \u0022t0k\u0022"}`,
	} {
		whole := string(RedactTokens([]byte(s)))
		for cut := 0; cut <= len(s); cut++ {
			var out bytes.Buffer
			r := NewRedactor(&out)
			_, _ = r.Write([]byte(s[:cut]))
			_, _ = r.Write([]byte(s[cut:]))
			require.NoError(t, r.Close())
			assert.Equal(t, whole, out.String(), "%q cut at %d", s, cut)
		}
	}
}

// A JSON reply held whole, through the Hub's redactReply: by key, exactly, at any depth.
func TestRedactSecretKeysInJSONReplies(t *testing.T) {
	in := `{"password":"hunter","db":{"PASSWORD":"x","apiKey":"y y","X-Api-Key":"z","auth":"plain",` +
		`"list":{"refresh_token":["a","b"]},"headers":[{"name":"client_secret","value":"v"}],` +
		`"tokens":"keep","secretary":"keep","author":"keep","total_token_usage":"keep","empty_password":"","n":{"token":7}}}`
	out, ok := redactReply(http.Header{"Content-Type": {"application/json"}}, []byte(in))
	require.True(t, ok)
	require.True(t, json.Valid(out))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	db := got["db"].(map[string]any)
	assert.Equal(t, sm, got["password"])
	for _, k := range []string{"PASSWORD", "apiKey", "X-Api-Key", "auth"} {
		assert.Equal(t, sm, db[k], k)
	}
	assert.Equal(t, []any{sm, sm}, db["list"].(map[string]any)["refresh_token"], "an array's strings take its key")
	assert.Equal(t, sm, db["headers"].([]any)[0].(map[string]any)["value"], `{"name": "client_secret", "value": ...}`)
	for _, k := range []string{"tokens", "secretary", "author", "total_token_usage"} {
		assert.Equal(t, "keep", db[k], k)
	}
	assert.Equal(t, "", db["empty_password"], "nothing to hide")
	assert.Equal(t, sm, db["n"].(map[string]any)["token"], "a number under a secret key is the marker too (a string)")

	// The same text in a reply that isn't JSON goes through the text rule.
	text, ok := redactReply(http.Header{"Content-Type": {"text/plain"}}, []byte("api_key=abc\n"))
	require.True(t, ok)
	assert.Equal(t, "api_key="+sm+"\n", string(text))
}

func TestIsSecretKey(t *testing.T) {
	for _, k := range []string{"password", "Password", "DB_PASSWORD", "passwd", "secret", "client_secret", "clientSecret",
		"token", "access_token", "accessToken", "refreshToken", "x-auth-token", "auth", "api_key", "apiKey", "X-Api-Key", "apikey",
		"private_key", "privateKey", "github.token", "passwords", "secrets", "secret_key", "SECRET_KEY", "secretKey",
		"AWS_SECRET_ACCESS_KEY", "access_key", "passphrase", "credential", "credentials", "api_keys"} {
		assert.True(t, isSecretKey(k), k)
	}
	for _, k := range []string{"tokens", "input_tokens", "total_token_usage", "token_count", "secretary", "author", "authority",
		"original_author", "oauth", "key", "name", "mytoken"} {
		assert.False(t, isSecretKey(k), k)
	}
}

// Security's review of #3733: (1) a quoted value is redacted to its closing quote, spaces and
// all, raw and inside a JSON string, and never past the end of that string; (2) more names.
func TestRedactSecretQuotedValuesAndMoreNames(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`PASSWORD="my pass phrase" next`, `PASSWORD="` + sm + `" next`},
		{`passphrase='two words' x`, `passphrase='` + sm + `' x`},
		{`{"password": "my pass phrase", "n": 1}`, `{"password": "` + sm + `", "n": 1}`},
		{`SECRET_KEY=django-insecure-abc`, `SECRET_KEY=` + sm},
		{`AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG`, `AWS_SECRET_ACCESS_KEY=` + sm},
		{`access_key: "AK 1"`, `access_key: "` + sm + `"`},
		{`{"credentials": "user:pw"}`, `{"credentials": "` + sm + `"}`},
		{`secretKey="s k"`, `secretKey="` + sm + `"`},
		{`password="unterminated and the body ends`, `password="` + sm},
	} {
		assert.Equal(t, c.want, string(RedactTokens([]byte(c.in))), c.in)
		enc, _ := json.Marshal(map[string]string{"content": c.in})
		out := RedactTokens(enc)
		require.True(t, json.Valid(out), "%s -> %s", enc, out)
		var got map[string]string
		require.NoError(t, json.Unmarshal(out, &got))
		if c.in != `password="unterminated and the body ends` { // in JSON it ends at the string's end
			assert.Equal(t, c.want, got["content"], "in JSON: %s", enc)
		} else {
			assert.Equal(t, `password="`+sm, got["content"])
		}
		for cut := 0; cut <= len(enc); cut++ {
			var b bytes.Buffer
			r := NewRedactor(&b)
			_, _ = r.Write(enc[:cut])
			_, _ = r.Write(enc[cut:])
			require.NoError(t, r.Close())
			require.Equal(t, string(out), b.String(), "%s cut at %d", enc, cut)
		}
	}
}

// (3) everything under a secret-named key, at any depth, plurals included; (4) a number or
// true/false there is the marker (a string: still JSON); null and "" stay.
func TestRedactSecretSubtrees(t *testing.T) {
	in := `{"token":{"a":"x","n":{"deep":["y",5]}},"password":{"value":"y"},"secrets":{"db":"z","port":5432},` +
		`"pin":{"password":12345678},"flag":{"secret":true},"none":{"password":null,"api_key":""},` +
		`"usage":{"input_tokens":12,"tokens":{"in":3}}}`
	out, ok := redactReply(http.Header{"Content-Type": {"application/json"}}, []byte(in))
	require.True(t, ok)
	require.True(t, json.Valid(out), string(out))
	want := `{"token":{"a":"` + sm + `","n":{"deep":["` + sm + `","` + sm + `"]}},"password":{"value":"` + sm + `"},` +
		`"secrets":{"db":"` + sm + `","port":"` + sm + `"},"pin":{"password":"` + sm + `"},"flag":{"secret":"` + sm + `"},` +
		`"none":{"password":null,"api_key":""},"usage":{"input_tokens":12,"tokens":{"in":3}}}`
	assert.JSONEq(t, want, string(out), "token counts are not secrets")
}

// A quiet stream flushed inside a secret value shows the marker, never the value, and the rest
// of a quoted value that arrives later is dropped up to its quote.
func TestRedactSecretFlushedMidValue(t *testing.T) {
	for _, c := range []struct{ first, rest, want string }{
		{"SECRET_KEY=abc", "def next", "SECRET_KEY=" + sm + " next"},
		{`PASSWORD="my pa`, `ss phrase" next`, `PASSWORD="` + sm + `" next`},
	} {
		var out bytes.Buffer
		r := NewRedactor(&out)
		_, _ = r.Write([]byte(c.first))
		require.NoError(t, r.Flush(true))
		_, _ = r.Write([]byte(c.rest))
		require.NoError(t, r.Close())
		assert.Equal(t, c.want, out.String(), c.first)
	}
}
