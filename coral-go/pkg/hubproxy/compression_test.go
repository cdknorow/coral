package hubproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatPage is a history page big enough for WriteJSONCompressed to gzip.
var chatPage = map[string]any{"messages": strings.Repeat("tool output line\n", 2000), "start": float64(0)}

// gzipJSONRemote serves chatPage the way Coral's /chat does and records the
// Accept-Encoding it was asked with.
func gzipJSONRemote(seen *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Accept-Encoding")
		httputil.WriteJSONCompressed(w, r, http.StatusOK, chatPage)
	})
}

// fetch requests path from the hub, setting Accept-Encoding explicitly so the
// Go client hands back the body exactly as relayed.
func fetch(t *testing.T, url, acceptEncoding string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	} else {
		req.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp, body
}

func decodeJSON(t *testing.T, body []byte, gzipped bool) map[string]any {
	t.Helper()
	var r io.Reader = bytes.NewReader(body)
	if gzipped {
		zr, err := gzip.NewReader(r)
		require.NoError(t, err)
		r = zr
	}
	var got map[string]any
	require.NoError(t, json.NewDecoder(r).Decode(&got))
	return got
}

// The self-hosted hub passes the browser's Accept-Encoding to the remote and
// relays the gzipped body and its Content-Encoding untouched: one encoding,
// decoded once by the browser.
func TestHTTPTargetRelaysGzipUnchanged(t *testing.T) {
	var seen string
	_, hub, _ := setup(t, gzipJSONRemote(&seen), nil)
	url := hub.URL + "/api/remote/a/api/sessions/live/x/chat?after=0"

	resp, body := fetch(t, url, "gzip, deflate, br")
	assert.Equal(t, "gzip, deflate, br", seen, "the remote sees the browser's Accept-Encoding")
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	assert.Contains(t, resp.Header.Values("Vary"), "Accept-Encoding")
	assert.Equal(t, chatPage, decodeJSON(t, body, true), "a single gzip decode yields the page")
	raw, _ := json.Marshal(chatPage)
	assert.Less(t, len(body), len(raw)/4)

	resp, body = fetch(t, url, "")
	assert.Equal(t, "identity", seen)
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Equal(t, chatPage, decodeJSON(t, body, false))
}

// Through the Cloud Hub tunnel the connector's request-header allowlist drops
// Accept-Encoding, so the machine answers plain JSON. A gzipped answer is still
// relayed with its Content-Encoding intact and is not encoded a second time.
func TestTunnelTargetCompression(t *testing.T) {
	var seen string
	local := http.NewServeMux()
	local.Handle("/api/chat", gzipJSONRemote(&seen))
	local.HandleFunc("/api/forced-gzip", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Accept-Encoding", "gzip")
		httputil.WriteJSONCompressed(w, r, http.StatusOK, chatPage)
	})
	hub := cloudHub(t, tunnelResolver{connectLocal(t, local, nil)}, nil)

	resp, body := fetch(t, hub.URL+"/api/remote/srv/api/chat", "gzip")
	assert.Empty(t, seen, "Accept-Encoding is not forwarded to the machine")
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Equal(t, chatPage, decodeJSON(t, body, false))

	resp, body = fetch(t, hub.URL+"/api/remote/srv/api/forced-gzip", "gzip")
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	assert.Equal(t, chatPage, decodeJSON(t, body, true), "relayed once-encoded, not stripped or re-encoded")
}
