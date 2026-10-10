package hubproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
