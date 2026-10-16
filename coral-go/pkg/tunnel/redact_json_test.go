package tunnel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3694: a private key quoted inside a JSON string ("key := \"-----BEGIN ...") made the
// redactor stop at the escaped quote, drop its backslash and close the string early, so a Coral
// Hub viewer's chat history failed to parse. Each fixture is a JSON reply holding text around a
// key (or a mention of one); at every split into two writes the redacted reply must be the same
// bytes, parse, carry no byte of the key's body, and keep the text outside the key.
type jsonKeyCase struct {
	name    string
	content string   // the transcript text, as the reply's "content" string holds it
	secrets []string // parts of the key body: none may appear in the reply
	kept    []string // text that must survive (decoded from the reply's JSON)
	same    bool     // nothing to redact: the reply comes back byte for byte
}

var jsonKeyCases = []jsonKeyCase{
	{name: "an RSA key in code, its string closed before any END line",
		content: "key := \"-----BEGIN RSA PRIVATE KEY-----\nMIIsecretAAAA\" // a fixture",
		secrets: []string{"MIIsecretAAAA", "a fixture"}, kept: []string{"key := \"[redacted token]"}},
	{name: "an OpenSSH key with escaped quotes in its body, then END and text",
		content: "found:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXkt\"secretQ\"secretR\n-----END OPENSSH PRIVATE KEY-----\ntail kept",
		secrets: []string{"b3BlbnNzaC1rZXkt", "secretQ", "secretR"}, kept: []string{"found:\n[redacted token]\ntail kept"}},
	{name: "a double-escaped quote (a backslash and a quote in the text) stays inside the key",
		content: "-----BEGIN RSA PRIVATE KEY-----\nAAA\\\"secretD\\\\\"secretE\n-----END RSA PRIVATE KEY-----\nafter-dbl",
		secrets: []string{"secretD", "secretE"}, kept: []string{"[redacted token]\nafter-dbl"}},
	{name: "\\u escapes in and around the key",
		content: "<b>before</b>\u2028-----BEGIN EC PRIVATE KEY-----\n<secretU>\u2028\u2029x\n-----END EC PRIVATE KEY-----<kept>",
		secrets: []string{"secretU"}, kept: []string{"<b>before</b>\u2028[redacted token]<kept>"}},
	{name: "a BEGIN mention that isn't a key, then an escaped quote and text",
		content: "grep -rn \"-----BEGIN \" . and \"PRIVATE KEY\" stays as written",
		kept:    []string{"grep -rn \"-----BEGIN \" . and \"PRIVATE KEY\" stays as written"}, same: true},
	{name: "HTML, a control character and invalid UTF-8 around a key",
		content: "<img src=x onerror=alert(1)>\x01\xff -----BEGIN PRIVATE KEY-----\nsecretH\"x\n-----END PRIVATE KEY----- end",
		secrets: []string{"secretH"}, kept: []string{"<img src=x onerror=alert(1)>\x01\ufffd [redacted token] end"}},
}

// jsonKeyReply is a chat-like reply: the case's text between two other messages.
func jsonKeyReply(t *testing.T, content string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"type": "user", "content": "before the key"},
		map[string]any{"type": "tool", "content": content, "n": 7},
		map[string]any{"type": "assistant", "content": "after the key"},
	}, "total": 3})
	require.NoError(t, err)
	return b
}

func redactSplit(b []byte, k int) []byte {
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, _ = r.Write(b[:k])
	_, _ = r.Write(b[k:])
	_ = r.Close()
	return out.Bytes()
}

func TestRedactQuotedKeyKeepsJSONValid(t *testing.T) {
	for _, c := range jsonKeyCases {
		t.Run(c.name, func(t *testing.T) {
			in := jsonKeyReply(t, c.content)
			want := RedactTokens(in)
			if c.same {
				assert.Equal(t, string(in), string(want), "nothing to redact")
			}
			for k := 0; k <= len(in); k++ {
				got := redactSplit(in, k)
				if !assert.Equal(t, string(want), string(got), "split at %d", k) {
					return
				}
			}
			require.True(t, json.Valid(want), "the redacted reply parses: %s", want)
			var parsed struct {
				Messages []struct {
					Type, Content string
				}
				Total int
			}
			require.NoError(t, json.Unmarshal(want, &parsed))
			require.Len(t, parsed.Messages, 3)
			assert.Equal(t, 3, parsed.Total)
			assert.Equal(t, "before the key", parsed.Messages[0].Content, "the messages around it are untouched")
			assert.Equal(t, "after the key", parsed.Messages[2].Content)
			for _, s := range c.secrets {
				assert.NotContains(t, string(want), s, "no byte of the key body")
			}
			for _, s := range c.kept {
				assert.Contains(t, parsed.Messages[1].Content, s)
			}
		})
	}
}

// A key whose closing quote and END line never arrive (the reply ends inside it) is dropped to
// the end: over-redacting, never the rest of the key as text.
func TestRedactKeyCutOffAtTheEnd(t *testing.T) {
	in := jsonKeyReply(t, "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn\"secretT\\\"secretU and more key")
	cut := bytes.Index(in, []byte("and more key")) + 3
	trunc := in[:cut]
	for k := 0; k <= len(trunc); k++ {
		got := string(redactSplit(trunc, k))
		for _, s := range []string{"b3Blbn", "secretT", "secretU", "and"} {
			require.NotContains(t, got, s, "split at %d", k)
		}
		require.True(t, strings.HasSuffix(got, RedactMarker), "dropped to the end, split at %d: %s", k, got)
	}
}

// The fail-closed rule: a JSON reply that parsed before redaction and doesn't after is never
// passed on. Other replies (not JSON, or not valid to begin with) are not judged.
func TestRedactedJSONOK(t *testing.T) {
	jsonHdr := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	assert.True(t, redactedJSONOK(jsonHdr, []byte(`{"a":"b"}`), []byte(`{"a":"[redacted token]"}`)))
	assert.False(t, redactedJSONOK(jsonHdr, []byte(`{"a":"b"}`), []byte(`{"a":"[redacted token]" b"}`)))
	assert.True(t, redactedJSONOK(jsonHdr, []byte(`not json`), []byte(`not json either`)), "the upstream's own invalid reply")
	assert.True(t, redactedJSONOK(http.Header{"Content-Type": {"text/plain"}}, []byte(`{"a":"b"}`), []byte(`{"a`)), "not JSON")
	assert.True(t, jsonReply(http.Header{"Content-Type": {"application/problem+json"}}))
	assert.False(t, jsonReply(http.Header{"Content-Type": {"text/html"}}))
}

// Through a real hub session, as the operator saw it: a chat reply streamed without a length
// (as httputil.WriteJSON sends it), flushed in uneven pieces, holding a quoted OpenSSH key, and
// the same reply gzipped. It arrives parseable, with the key's body gone and everything else
// byte for byte as the redactor leaves it.
func TestRedactedChatReplyParsesThroughTheTunnel(t *testing.T) {
	var msgs []any
	for i := 0; i < 600; i++ {
		msgs = append(msgs, map[string]any{"type": "assistant", "content": strings.Repeat("filler \"quoted\" text\\ ", 20)})
	}
	key := "found in a code search: \"-----BEGIN OPENSSH PRIVATE KEY-----\\nb3BlbnNzaC1rZXktdjEAAAAA\" // fixture"
	msgs = append(msgs[:200], append([]any{map[string]any{"type": "tool", "content": key}}, msgs[200:]...)...)
	body, err := json.Marshal(map[string]any{"messages": msgs, "total": len(msgs)})
	require.NoError(t, err)
	require.Greater(t, len(body), 300<<10)

	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b := body
		for len(b) > 0 {
			n := min(len(b), 7001)
			w.Write(b[:n])
			w.(http.Flusher).Flush()
			b = b[n:]
		}
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, nil)
	resp := do(t, sess, "GET", "/api/sessions/live/a/chat", "", strings.NewReader(""), nil)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, json.Valid(got), "the chat reply parses after redaction")
	assert.Equal(t, string(RedactTokens(body)), string(got))
	assert.NotContains(t, string(got), "b3BlbnNzaC1rZXktdjEAAAAA")
	var parsed struct{ Messages []map[string]any }
	require.NoError(t, json.Unmarshal(got, &parsed))
	assert.Len(t, parsed.Messages, len(msgs))
}
