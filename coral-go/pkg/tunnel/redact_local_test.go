package tunnel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/redact"
)

// #3560 through a real hub session: a View reply's tokens are replaced before it leaves the
// computer (small replies whole, large ones streamed with tokens across chunk boundaries),
// exempt routes and binary types pass byte for byte, a Range is never sent on for a redacted
// route, no stale length is announced, non-View replies are untouched, and the setting off
// turns it all off.
func TestRedactThroughTheTunnel(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	key := pemKey("RSA ", 30, "\n")
	small, _ := json.Marshal(map[string]string{"output": "export GITHUB_TOKEN=" + tok + "\n"})
	var large bytes.Buffer
	for large.Len() < 300<<10 {
		large.WriteString(strings.Repeat("x", 1000+large.Len()%977) + " " + tok + " and Authorization: Bearer " + fake("", 40, alnum) + "\n")
	}
	large.WriteString(key + "\nend\n")
	artifact := []byte("artifact bytes stay exactly as stored: " + tok)
	var sawRange atomic.Value
	sawRange.Store("")

	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange.Store(r.Header.Get("Range"))
		switch {
		case r.URL.Path == "/api/sessions/live/a/capture":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(small)))
			w.Write(small)
		case r.URL.Path == "/api/sessions/live/a/events":
			// Larger than one whole-reply write, with a known length: streamed through the redactor.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(large.Len()))
			w.Write(large.Bytes())
		case r.URL.Path == "/api/sessions/live/a/chat":
			w.Header().Set("Content-Type", "application/json")
			// Written in uneven pieces with flushes, so tokens straddle the relay's chunks.
			b := large.Bytes()
			for len(b) > 0 {
				n := min(len(b), 7001)
				w.Write(b[:n])
				w.(http.Flusher).Flush()
				b = b[n:]
			}
		case strings.HasPrefix(r.URL.Path, "/api/artifacts/"):
			w.Header().Set("Content-Type", "text/plain")
			w.Write(artifact)
		case r.URL.Path == "/api/sessions/live/a/file-original":
			w.Header().Set("Content-Type", "image/png")
			w.Write(artifact)
		case r.URL.Path == "/api/sessions/launch":
			w.Header().Set("Content-Type", "application/json")
			w.Write(small)
		}
	})
	redactOn := atomic.Bool{}
	redactOn.Store(true)
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) { l.RedactTokens = redactOn.Load })

	get := func(method, path string, hdr http.Header) (*http.Response, string) {
		t.Helper()
		resp := do(t, sess, method, path, "", strings.NewReader(""), hdr)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err, path)
		return resp, string(b)
	}

	resp, body := get("GET", "/api/sessions/live/a/capture", http.Header{"Range": {"bytes=10-"}})
	assert.Equal(t, 200, resp.StatusCode)
	assert.NotContains(t, body, tok)
	assert.Contains(t, body, redact.Marker)
	assert.True(t, json.Valid([]byte(body)), body)
	assert.Equal(t, strconv.Itoa(len(small)), resp.Header.Get(BodyLengthHeader), "the upstream length stays advisory for the hub's cap (#3568)")
	assert.Empty(t, resp.Header.Get("Content-Length"), "the body is re-framed: no Content-Length")
	assert.Equal(t, "", sawRange.Load(), "a Range isn't sent on for a redacted route")

	resp, body = get("GET", "/api/sessions/live/a/chat", nil)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(BodyLengthHeader), "a streamed reply of unknown length announces none (#3568)")
	assert.NotContains(t, body, tok)
	assert.NotContains(t, body, "PRIVATE KEY")
	assert.NotContains(t, body, key[100:160], "no part of the key")
	assert.Equal(t, string(redact.Tokens(large.Bytes())), body, "the stream gives what redacting the whole gives")

	// A large reply of known length streams through the redactor and keeps the upstream length.
	resp, body = get("GET", "/api/sessions/live/a/events", nil)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, strconv.Itoa(large.Len()), resp.Header.Get(BodyLengthHeader), "a known upstream length stays (#3568)")
	assert.NotContains(t, body, tok)

	hex := strings.Repeat("a", 64)
	_, body = get("GET", "/api/artifacts/"+hex, http.Header{"Range": {"bytes=0-"}})
	assert.Equal(t, string(artifact), body, "exempt: byte for byte")
	assert.Equal(t, "bytes=0-", sawRange.Load(), "and its Range goes through")

	_, body = get("GET", "/api/sessions/live/a/file-original", nil)
	assert.Equal(t, string(artifact), body, "an image passes untouched")

	_, body = get("POST", "/api/sessions/launch", nil)
	assert.Contains(t, body, tok, "only View replies are redacted")

	redactOn.Store(false)
	_, body = get("GET", "/api/sessions/live/a/capture", nil)
	assert.Contains(t, body, tok, "the setting off: unchanged")
}

const redactGolden = "testdata/view_redaction.golden"

// Every View route with how its replies are treated. Deny by default: a new View route is
// "redact" unless someone adds it to redactExempt with a reason; this file changes either way,
// so the decision is reviewed. Update: REDACT_GOLDEN_UPDATE=1 go test ./pkg/tunnel -run ViewRedactionCensus
func TestViewRedactionCensus(t *testing.T) {
	var lines []string
	views := map[string]bool{}
	for _, rule := range RoutePatterns() {
		f := strings.Fields(rule)
		if len(f) != 3 || f[2] != "view" || f[0] == "HEAD" {
			continue
		}
		key := f[0] + " " + f[1]
		if views[key] {
			continue
		}
		views[key] = true
		class := RedactionClass(f[0], f[1])
		require.NotEqual(t, "exempt: ", class, "an exemption needs a reason: %s", key)
		lines = append(lines, key+" "+class)
	}
	for key := range redactExempt {
		require.True(t, views[key], "exempt but not a View route (stale entry?): %s", key)
	}
	for key := range redactLater {
		require.True(t, views[key], "listed for later but not a View route: %s", key)
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"
	if os.Getenv("REDACT_GOLDEN_UPDATE") == "1" {
		require.NoError(t, os.WriteFile(redactGolden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(redactGolden)
	require.NoError(t, err)
	require.Equal(t, string(want), got, "View route redaction changed: review it, then REDACT_GOLDEN_UPDATE=1 go test ./pkg/tunnel -run ViewRedactionCensus")
}

func TestTextReply(t *testing.T) {
	for ct, want := range map[string]bool{
		"application/json": true, "application/json; charset=utf-8": true, "text/plain": true, "text/event-stream": true,
		"application/x-ndjson": true, "application/octet-stream": true, "": true, "image/svg+xml": true, "garbage;;": true,
		"image/png": false, "application/pdf": false, "font/woff2": false, "application/zip": false, "video/mp4": false,
	} {
		assert.Equal(t, want, textReply(ct), ct)
	}
}

// #3564 (Security's note on #3560): a gzip reply from the local app is decoded by the connector,
// and its Content-Length is the compressed size. The redacted reply streams it whole instead of
// reading only that many bytes of the decoded body.
func TestRedactDecodedReplyWhole(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	plain, _ := json.Marshal(map[string]string{"output": strings.Repeat("compressible line of output\n", 400) + "export GH=" + tok + "\nend of output"})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	require.NoError(t, zw.Close())
	require.Less(t, gz.Len(), len(plain)/4, "the compressed size is far below the body's")
	require.Less(t, len(plain), wholeReplyMax, "small enough for the whole-reply path")

	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(gz.Len()))
		w.Write(gz.Bytes())
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, nil)
	resp := do(t, sess, "GET", "/api/sessions/live/a/capture", "", strings.NewReader(""), nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, string(redact.Tokens(plain)), string(body), "the whole decoded body, redacted")
	assert.Empty(t, resp.Header.Get(BodyLengthHeader), "a decoded body's length isn't known, so none is announced")
	assert.True(t, json.Valid(body))
	assert.Contains(t, string(body), "end of output")
	assert.NotContains(t, string(body), tok)
}
