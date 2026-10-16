package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3682 (finding 4): every /api/ and /ws response is no-store unless its handler chooses
// otherwise; the page itself is untouched.
func TestAPIResponsesAreNotStored(t *testing.T) {
	h := noStoreAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/artifacts/x" {
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable") // its own choice
		}
	}))
	for path, want := range map[string]string{
		"/api/health": "no-store", "/api/board/team/messages": "no-store", "/ws/coral": "no-store",
		"/api/artifacts/x": "private, max-age=31536000, immutable", "/": "", "/static/app.js": "",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		assert.Equal(t, want, rec.Header().Get("Cache-Control"), path)
	}
}

// The QR code can encode a sign-in link: never kept.
func TestQRCodeIsNotStored(t *testing.T) {
	r := newE2ERemote(t, "qr", nil)
	req := httptest.NewRequest("GET", "/api/system/qr?url=http%3A%2F%2F127.0.0.1%3A8420%2F", nil)
	req.RemoteAddr, req.Host = "127.0.0.1:50000", "127.0.0.1:8420"
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	r.srv.Router().ServeHTTP(rec, req)
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"), "status %d", rec.Code)
}
