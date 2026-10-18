package startup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4602: the main listener refuses a request whose headers are over 64 KiB with 431, before any handler; an ordinary request, and
// a request with a few KiB of headers (a long cookie, a proxy's forwarding chain), still reach it.
func TestMainListenerCapsRequestHeaders(t *testing.T) {
	reached := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(http.StatusNoContent) }))
	srv.Config = newHTTPServer(srv.Config.Handler)
	srv.Start()
	t.Cleanup(srv.Close)
	get := func(headerBytes int) int {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		if headerBytes > 0 {
			req.Header.Set("X-Pad", strings.Repeat("a", headerBytes))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusNoContent, get(0))
	assert.Equal(t, http.StatusNoContent, get(8<<10), "a few KiB of headers is fine")
	assert.Equal(t, http.StatusRequestHeaderFieldsTooLarge, get(maxRequestHeaderBytes+8<<10), "over the cap (Go allows 4 KiB of buffer slop): 431")
	assert.Equal(t, 2, reached, "the refused request never reached the handler")
}
