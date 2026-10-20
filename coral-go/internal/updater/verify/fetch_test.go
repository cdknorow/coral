package verify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A test server stands for GitHub: its own host is the one allowed, over http (the test's scheme).
func testFetcher(srv *httptest.Server, allowed ...string) *Fetcher {
	u, _ := url.Parse(srv.URL)
	hosts := append([]string{u.Hostname()}, allowed...)
	return &Fetcher{Scheme: "http", Allow: func(h string) bool {
		for _, a := range hosts {
			if h == a {
				return true
			}
		}
		return false
	}}
}

func TestFetchReadsAllowedHostsWithinTheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }))
	defer srv.Close()
	got, err := testFetcher(srv).Get(context.Background(), srv.URL+"/x", 5)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestFetchRefusesABodyOverTheCapWhetherDeclaredOrStreamed(t *testing.T) {
	declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("a", 100))) }))
	defer declared.Close()
	_, err := testFetcher(declared).Get(context.Background(), declared.URL, 10)
	assert.Error(t, err, "a declared length over the cap")
	streamed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush() // no Content-Length: chunked
		for i := 0; i < 20; i++ {
			_, _ = w.Write([]byte(strings.Repeat("a", 10)))
			w.(http.Flusher).Flush()
		}
	}))
	defer streamed.Close()
	_, err = testFetcher(streamed).Get(context.Background(), streamed.URL, 50)
	assert.Error(t, err, "a streamed body over the cap")
}

func TestFetchRefusesARedirectOffTheAllowList(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("evil")) }))
	defer evil.Close()
	evilURL, _ := url.Parse(evil.URL)
	// The redirecting server is on 127.0.0.1; the target is "localhost", a different host name for the same machine, which is not allowed.
	target := strings.Replace(evil.URL, evilURL.Hostname(), "localhost", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }))
	defer srv.Close()
	_, err := testFetcher(srv).Get(context.Background(), srv.URL, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
	// The same redirect is followed when the target host is on the list.
	got, err := testFetcher(srv, "localhost").Get(context.Background(), srv.URL, 100)
	require.NoError(t, err)
	assert.Equal(t, "evil", string(got))
}

func TestFetchRefusesLoopsOtherSchemesCredentialsAndErrors(t *testing.T) {
	var self *httptest.Server
	self = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, self.URL+"/again", http.StatusFound) }))
	defer self.Close()
	_, err := testFetcher(self).Get(context.Background(), self.URL, 100)
	assert.Error(t, err, "a redirect loop")
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer ok.Close()
	f := testFetcher(ok)
	_, err = f.Get(context.Background(), strings.Replace(ok.URL, "http://", "https://", 1), 100)
	assert.Error(t, err, "another scheme than the one required")
	u, _ := url.Parse(ok.URL)
	u.User = url.UserPassword("a", "b")
	_, err = f.Get(context.Background(), u.String(), 100)
	assert.Error(t, err, "credentials in the URL")
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	_, err = testFetcher(missing).Get(context.Background(), missing.URL, 100)
	assert.Error(t, err, "a 404 is not a body")
	// The real defaults: only GitHub's hosts, over https.
	d := &Fetcher{}
	for _, bad := range []string{"https://evil.example/x", "http://github.com/x", "https://github.com.evil.example/x", "https://raw.githubusercontent.com/x"} {
		_, err := d.Get(context.Background(), bad, 10)
		assert.Error(t, err, bad)
	}
}
