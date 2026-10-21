package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/pkg/frontend"
)

// v1.4.12 (#3168): a browser that cached a pre-1.4.12 inline artifact response has the origin's
// HTTP cache cleared once. The marker is only set where the browser honours the header.
func TestIndexPurgesTheCacheOnceInASecureContext(t *testing.T) {
	tmpl, err := frontend.ParseIndex()
	require.NoError(t, err)
	s := &Server{cfg: &config.Config{}, indexTmpl: tmpl}
	serve := func(host string, tlsOn bool, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		if tlsOn {
			req.TLS = &tls.ConnectionState{}
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		s.serveIndex(rr, req)
		return rr
	}
	marker := func(rr *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rr.Result().Cookies() {
			if c.Name == cacheMarkerCookie {
				return c
			}
		}
		return nil
	}

	for _, host := range []string{"127.0.0.1:8420", "localhost:8420", "[::1]:8420", "coral.localhost"} {
		first := serve(host, false, nil)
		require.Equal(t, http.StatusOK, first.Code, host)
		assert.Equal(t, `"cache"`, first.Header().Get("Clear-Site-Data"), "%s: purge without the marker", host)
		c := marker(first)
		require.NotNil(t, c, "%s: marker set on loopback", host)
		assert.True(t, c.HttpOnly, "HttpOnly")
		assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
		assert.Equal(t, 30*24*3600, c.MaxAge, "a flag: 30 days (#3682)")

		again := serve(host, false, nil, &http.Cookie{Name: cacheMarkerCookie, Value: c.Value})
		assert.Empty(t, again.Header().Get("Clear-Site-Data"), "%s: no purge once marked", host)
		assert.Nil(t, marker(again), "%s: marker not re-sent", host)
	}

	// https (direct or behind a TLS proxy): purged once, marker Secure.
	for name, req := range map[string]func() *httptest.ResponseRecorder{
		"tls": func() *httptest.ResponseRecorder { return serve("coral.example.com", true, nil) },
		"proxy": func() *httptest.ResponseRecorder {
			return serve("coral.example.com", false, map[string]string{"X-Forwarded-Proto": "https"})
		},
	} {
		rr := req()
		assert.Equal(t, `"cache"`, rr.Header().Get("Clear-Site-Data"), name)
		c := marker(rr)
		require.NotNil(t, c, name)
		assert.True(t, c.Secure, "%s: Secure over https", name)
	}

	// Plain http on a LAN address: the browser ignores the header, so no marker; the purge is
	// offered again next time, and normal revalidation (304) is untouched.
	lan := serve("192.168.1.20:8420", false, nil)
	assert.Equal(t, `"cache"`, lan.Header().Get("Clear-Site-Data"))
	assert.Nil(t, marker(lan), "no marker where the purge is ignored")
	etag := lan.Header().Get("ETag")
	require.NotEmpty(t, etag)
	assert.Equal(t, http.StatusNotModified, serve("192.168.1.20:8420", false, map[string]string{"If-None-Match": etag}).Code)

	// In a secure context the purging load is a full page even when the browser revalidates.
	assert.Equal(t, http.StatusOK, serve("127.0.0.1:8420", false, map[string]string{"If-None-Match": etag}).Code)
	assert.Equal(t, http.StatusNotModified, serve("127.0.0.1:8420", false, map[string]string{"If-None-Match": etag}, &http.Cookie{Name: cacheMarkerCookie, Value: "1412"}).Code)
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST:8420": true, "app.localhost": true, "127.0.0.1": true, "127.3.4.5:1": true, "[::1]:8420": true,
		"192.168.1.2:8420": false, "example.com": false, "localhost.example.com": false, "": false, "0.0.0.0:8420": false,
	} {
		assert.Equal(t, want, loopbackHost(host), host)
	}
}
