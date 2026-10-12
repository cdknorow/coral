package hubproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

func TestFilterKeepsASingleContentTypeAndSanitisedFilename(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header["Content-Type"] = []string{"text/plain; charset=utf-8", "text/html"} // duplicate header
	resp.Header.Set("Content-Disposition", `attachment; filename="../../etc/pa ss\wd<script>.txt"`)
	require.NoError(t, StrictResponseFilter(resp))
	assert.Equal(t, []string{"text/plain; charset=utf-8"}, resp.Header.Values("Content-Type"), "duplicates cannot survive")
	assert.Equal(t, "attachment; filename=wd_script_.txt", resp.Header.Get("Content-Disposition"))

	resp = &http.Response{Header: http.Header{}}
	resp.Header.Set("Content-Type", "text/html")
	resp.Header.Set("Content-Disposition", `inline; filename="x.html"`)
	require.NoError(t, StrictResponseFilter(resp))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "attachment; filename=x.html", resp.Header.Get("Content-Disposition"))

	resp = &http.Response{Header: http.Header{}}
	resp.Header.Set("Content-Type", "application/json")
	require.NoError(t, StrictResponseFilter(resp))
	assert.Empty(t, resp.Header.Get("Content-Disposition"), "no filename, no disposition for an allowed type")
}

func TestAllowedOriginsAreValidated(t *testing.T) {
	for _, bad := range []string{"*", "*.com", "*:*", "*.*", "https://hub.example.com", "hub.example.com/x", "", "*.example", "?", "[a-z]*.example.com"} {
		assert.Error(t, ValidateOptions(Options{AllowedOrigins: []string{bad}}), "accepted %q", bad)
	}
	for _, ok := range []string{"hub.example.com", "*.example.com", "hub.example.com:8443", "hub.example.com:*", "localhost:3000"} {
		assert.NoError(t, ValidateOptions(Options{AllowedOrigins: []string{ok}}), "rejected %q", ok)
	}
	// A handler built with bad options refuses every WebSocket upgrade.
	h := NewWithOptions(nil, Options{AllowedOrigins: []string{"*"}})
	_, _, ok := h.originPatterns(httptest.NewRequest("GET", "/", nil), nil)
	assert.False(t, ok)
}

// Artifacts are agent-authored and content-addressed: whatever type the agent chose, only the safe media types
// are served inline, everything else becomes an octet-stream attachment, and nothing is cacheable by a browser.
func TestArtifactResponsesAreNeutralisedAndNeverCached(t *testing.T) {
	path := "/api/artifacts/" + strings.Repeat("c", 64)
	mk := func(ct string) *http.Response {
		req := httptest.NewRequest("GET", path, nil)
		resp := &http.Response{Header: http.Header{}, Request: req, ContentLength: 5, Body: io.NopCloser(strings.NewReader("hello"))}
		resp.Header.Set("Content-Type", ct)
		resp.Header.Set("Content-Disposition", `inline; filename="../a b.html"`)
		resp.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
		resp.Header.Set("Etag", `"abc"`)
		resp.Header.Set("Expires", "Wed, 21 Oct 2037 07:28:00 GMT")
		return resp
	}
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "image/svg+xml", "application/javascript", "text/javascript", "application/xhtml+xml", "application/pdf", "text/xml", "application/x-unknown", ""} {
		resp := mk(ct)
		require.NoError(t, StrictResponseFilter(resp))
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"), ct)
		assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment"), ct)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "sandbox; default-src 'none'", resp.Header.Get("Content-Security-Policy"))
		assert.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"), ct)
		assert.Empty(t, resp.Header.Get("Etag")+resp.Header.Get("Expires"), "no validators or expiry that would make a cache keep it")
	}
	for _, ct := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "text/plain; charset=utf-8", "application/json"} {
		resp := mk(ct)
		require.NoError(t, StrictResponseFilter(resp))
		assert.Equal(t, ct, resp.Header.Get("Content-Type"), "safe types may render inline")
		assert.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"), ct)
	}
}

func TestArtifactSizeIsCappedByTheHub(t *testing.T) {
	path := "/api/artifacts/" + strings.Repeat("d", 64)
	// declared too large -> clear 413, the body is never relayed
	resp := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
		ContentLength: MaxArtifactBytes + 1, Body: io.NopCloser(strings.NewReader("x"))}
	resp.Header.Set("Content-Type", "image/png")
	require.NoError(t, StrictResponseFilter(resp))
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	assert.JSONEq(t, `{"error":"artifact_too_large"}`, string(b))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	// exactly at the limit is fine
	resp = &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
		ContentLength: MaxArtifactBytes, Body: io.NopCloser(strings.NewReader("x"))}
	require.NoError(t, StrictResponseFilter(resp))
	assert.Equal(t, 200, resp.StatusCode)
	// undeclared length -> cut off with an error once past the limit, never unbounded
	resp = &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
		ContentLength: -1, Body: io.NopCloser(io.LimitReader(zeroReader{}, MaxArtifactBytes+4096))}
	require.NoError(t, StrictResponseFilter(resp))
	n, err := io.Copy(io.Discard, resp.Body)
	assert.Error(t, err)
	assert.EqualValues(t, MaxArtifactBytes, n)
	// other routes are not capped by this rule
	other := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", "/api/sessions/live/x/capture", nil),
		ContentLength: MaxArtifactBytes + 1, Body: io.NopCloser(strings.NewReader("x"))}
	require.NoError(t, StrictResponseFilter(other))
	assert.Equal(t, 200, other.StatusCode)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// The connector's advisory length (the tunnel re-frames bodies, so there is no Content-Length) lets the hub answer
// a clear 413 for a huge artifact; the header itself is never relayed to the browser.
func TestAdvisoryLengthGivesAClear413AndIsNotRelayed(t *testing.T) {
	path := "/api/artifacts/" + strings.Repeat("f", 64)
	resp := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
		ContentLength: -1, Body: io.NopCloser(strings.NewReader("x"))}
	resp.Header.Set("Content-Type", "image/png")
	resp.Header.Set(tunnel.BodyLengthHeader, strconv.FormatInt(MaxArtifactBytes+1, 10))
	require.NoError(t, StrictResponseFilter(resp))
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(tunnel.BodyLengthHeader))
	// a small claim does not widen the cap: a lying connector is still cut off at the limit
	resp = &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
		ContentLength: -1, Body: io.NopCloser(io.LimitReader(zeroReader{}, MaxArtifactBytes+10))}
	resp.Header.Set(tunnel.BodyLengthHeader, "10")
	require.NoError(t, StrictResponseFilter(resp))
	n, err := io.Copy(io.Discard, resp.Body)
	assert.Error(t, err)
	assert.EqualValues(t, MaxArtifactBytes, n)
	// the header is stripped from every response, artifact or not
	other := &http.Response{Header: http.Header{}, Request: httptest.NewRequest("GET", "/api/x", nil), ContentLength: -1, Body: http.NoBody}
	other.Header.Set(tunnel.BodyLengthHeader, "5")
	require.NoError(t, StrictResponseFilter(other))
	assert.Empty(t, other.Header.Get(tunnel.BodyLengthHeader))
}

// A body of exactly the limit is relayed whole and ends cleanly; one byte more is an error.
func TestArtifactCapBoundary(t *testing.T) {
	path := "/api/artifacts/" + strings.Repeat("9", 64)
	mk := func(size int64) *http.Response {
		r := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path, nil),
			ContentLength: -1, Body: io.NopCloser(io.LimitReader(zeroReader{}, size))}
		r.Header.Set("Content-Type", "image/png")
		return r
	}
	exact := mk(MaxArtifactBytes)
	require.NoError(t, StrictResponseFilter(exact))
	n, err := io.Copy(io.Discard, exact.Body)
	assert.NoError(t, err, "exactly the limit is fine")
	assert.EqualValues(t, MaxArtifactBytes, n)
	over := mk(MaxArtifactBytes + 1)
	require.NoError(t, StrictResponseFilter(over))
	n, err = io.Copy(io.Discard, over.Body)
	assert.Error(t, err, "one byte over is refused")
	assert.EqualValues(t, MaxArtifactBytes, n)
	small := mk(10)
	require.NoError(t, StrictResponseFilter(small))
	n, err = io.Copy(io.Discard, small.Body)
	assert.NoError(t, err)
	assert.EqualValues(t, 10, n)
}

// An Agent UI panel is 2 MiB at most; a larger one is a clear 413, an exact-limit body ends cleanly, and the content route is the only
// panel route capped.
func TestPanelContentIsCappedAtTwoMiB(t *testing.T) {
	path := "/api/agent/ui/panel_1/content"
	mk := func(size int64, declared string) *http.Response {
		r := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", path+"?session_id=s", nil),
			ContentLength: -1, Body: io.NopCloser(io.LimitReader(zeroReader{}, size))}
		r.Header.Set("Content-Type", "text/html")
		if declared != "" {
			r.Header.Set(tunnel.BodyLengthHeader, declared)
		}
		return r
	}
	over := mk(10, strconv.FormatInt(MaxPanelBytes+1, 10))
	require.NoError(t, StrictResponseFilter(over))
	assert.Equal(t, http.StatusRequestEntityTooLarge, over.StatusCode)
	b, _ := io.ReadAll(over.Body)
	assert.JSONEq(t, `{"error":"panel_too_large"}`, string(b))
	exact := mk(MaxPanelBytes, "")
	require.NoError(t, StrictResponseFilter(exact))
	n, err := io.Copy(io.Discard, exact.Body)
	assert.NoError(t, err)
	assert.EqualValues(t, MaxPanelBytes, n)
	liar := mk(MaxPanelBytes+5, "10")
	require.NoError(t, StrictResponseFilter(liar))
	n, err = io.Copy(io.Discard, liar.Body)
	assert.Error(t, err, "a connector that lies about the length is still cut off")
	assert.EqualValues(t, MaxPanelBytes, n)
	// scriptable type still becomes a download, never cached
	assert.Equal(t, "application/octet-stream", exact.Header.Get("Content-Type"))
	assert.Equal(t, "private, no-store", exact.Header.Get("Cache-Control"))
	// other agent/ui routes are not capped by this rule
	other := &http.Response{Header: http.Header{}, StatusCode: 200, Request: httptest.NewRequest("GET", "/api/agent/ui?session_id=s", nil), ContentLength: MaxPanelBytes + 1, Body: io.NopCloser(strings.NewReader("x"))}
	require.NoError(t, StrictResponseFilter(other))
	assert.Equal(t, 200, other.StatusCode)
}
