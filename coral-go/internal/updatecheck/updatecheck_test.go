package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3660 item 4: only a strictly newer release is offered; a newer, dev or prerelease build is
// never told to move to an older one, and a build that isn't a version is never offered one.
func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"1.4.53", "1.4.52", true},
		{"v1.5.0", "1.4.99", true},
		{"2.0.0", "1.99.99", true},
		{"1.4.52", "1.4.52", false},
		{"1.4.51", "1.4.52", false},     // a build ahead of the latest release
		{"1.4.52", "1.4.53-dev", false}, // a dev build of the next release
		{"1.4.52", "1.4.53-rc1", false}, // a release candidate
		{"1.4.53", "1.4.53-rc1", true},  // its release is newer
		{"1.4.53-rc2", "1.4.53-rc1", true},
		{"1.4.53-rc.10", "1.4.53-rc.9", true}, // numeric identifiers by value
		{"1.4.53-1", "1.4.53-alpha", false},   // numbers below words
		{"1.4.10", "1.4.9", true},             // not a string comparison
		{"1.4.53", "dev", false},
		{"1.4.53", "", false},
		{"", "1.4.52", false},
		{"not-a-version", "1.4.52", false},
	} {
		assert.Equal(t, c.want, Newer(c.latest, c.current), "Newer(%q, %q)", c.latest, c.current)
	}
}

func serve(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func tag(s string) string {
	b, _ := json.Marshal(map[string]any{"tag_name": s, "draft": false, "prerelease": false, "html_url": tagPagePrefix + s})
	return string(b)
}

// Items 4 and 5: a 200 only, a capped body, and a version-shaped tag.
func TestLatest(t *testing.T) {
	ctx := context.Background()
	v, err := Latest(ctx, serve(t, 200, tag("v1.4.53")))
	require.NoError(t, err)
	assert.Equal(t, "1.4.53", v)

	_, err = Latest(ctx, serve(t, 403, tag("v9.9.9")))
	assert.Error(t, err, "a rate-limit 403 is an error, whatever its body says")

	huge := `{"body":"` + strings.Repeat("x", MaxBody) + `","tag_name":"v9.9.9"}`
	_, err = Latest(ctx, serve(t, 200, huge))
	assert.Error(t, err, "nothing past MaxBody is read")

	for _, bad := range []string{"", "v1.2", "1.2.3.4", "v1.2.3<script>", "v1.2.3-rc 1", "v1.2.3\n", "');alert(1)//", "v1.2.3-" + strings.Repeat("a", 41)} {
		_, err := Latest(ctx, serve(t, 200, tag(bad)))
		assert.ErrorIs(t, err, ErrBadTag, "%q", bad)
	}
	v, err = Latest(ctx, serve(t, 200, tag("v1.4.53-rc.1")))
	require.NoError(t, err)
	assert.Equal(t, "1.4.53-rc.1", v)
}

func TestAvailable(t *testing.T) {
	ctx := context.Background()
	v, err := Available(ctx, serve(t, 200, tag("v1.4.53")), "1.4.52")
	require.NoError(t, err)
	assert.Equal(t, "1.4.53", v)
	v, err = Available(ctx, serve(t, 200, tag("v1.4.51")), "1.4.52")
	require.NoError(t, err)
	assert.Empty(t, v, "an older release is never offered")
}

// #4133: the update check reads a public endpoint and never carries a credential from the
// environment, not to the feed and not to wherever it redirects (and since #5025 it redirects nowhere off the host).
func TestLatestSendsNoCredential(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "FAKE-TOKEN-FOR-TEST")
	t.Setenv("GH_TOKEN", "FAKE-TOKEN-FOR-TEST")
	var seen []http.Header
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		_, _ = w.Write([]byte(tag("v9.9.9")))
	}))
	defer target.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer feed.Close()
	// Since #5025 a redirect to another host is not followed at all, so the credential cannot reach it even by a bug: only the feed is asked.
	v, err := Latest(context.Background(), feed.URL)
	require.Error(t, err)
	assert.Empty(t, v)
	require.Len(t, seen, 1, "the feed answered; the redirect target was never asked")
	for i, h := range seen {
		assert.Empty(t, h.Get("Authorization"), "request %d carries no Authorization", i)
		for k, vals := range h {
			for _, val := range vals {
				assert.NotContains(t, val, "FAKE-TOKEN", "request %d header %s", i, k)
			}
		}
	}
}

// #5025: a redirect from the feed is followed on the same host (GitHub's answer for a renamed repository) and nowhere else.
func TestLatestFollowsOnlySameHostRedirects(t *testing.T) {
	ctx := context.Background()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tag("v9.9.9"))) // what a hostile feed would like the check to read
	}))
	t.Cleanup(other.Close)
	var self string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, self+"/final", http.StatusMovedPermanently)
		case "/final":
			w.Write([]byte(tag("v1.4.53")))
		case "/away":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/loop":
			http.Redirect(w, r, self+"/loop", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	self = srv.URL

	v, err := Latest(ctx, self+"/same")
	require.NoError(t, err)
	assert.Equal(t, "1.4.53", v, "a redirect on the same host is followed")

	v, err = Latest(ctx, self+"/away")
	require.Error(t, err, "a redirect to another host is refused")
	assert.Empty(t, v)
	assert.Contains(t, err.Error(), "another host or scheme")

	_, err = Latest(ctx, self+"/loop")
	require.Error(t, err, "a redirect loop ends")

	// https to http on the same host is a downgrade
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/x", http.StatusFound)
	}))
	t.Cleanup(tls.Close)
	_, err = Latest(ctx, tls.URL) // the test server's certificate is not trusted by the check: it fails before any redirect, which is also a refusal
	require.Error(t, err)
}

// #5568: every read of releases/latest checks the reply's page, not only the channel feeds: the server's log line and FetchLatestVersion included.
func TestLatestRefusesAReplyThatIsNotThisRepositorysRelease(t *testing.T) {
	ctx := context.Background()
	body := func(page string, draft, pre bool) string {
		b, _ := json.Marshal(map[string]any{"tag_name": "v1.4.150", "draft": draft, "prerelease": pre, "html_url": page})
		return string(b)
	}
	good := tagPagePrefix + "v1.4.150"
	v, err := Latest(ctx, serve(t, 200, body(good, false, false)))
	require.NoError(t, err)
	assert.Equal(t, "1.4.150", v)
	for name, b := range map[string]string{
		"no page":         body("", false, false),
		"only a tag":      `{"tag_name":"v1.4.150"}`,
		"another owner":   body("https://github.com/cdknorow/coral/releases/tag/v1.4.150", false, false),
		"look-alike repo": body("https://github.com/subgentic/coral-evil/releases/tag/v1.4.150", false, false),
		"another tag":     body(tagPagePrefix+"v1.4.149", false, false),
		"http":            body("http://github.com/subgentic/coral/releases/tag/v1.4.150", false, false),
		"a draft":         body(good, true, false),
		"a pre-release":   body(good, false, true),
	} {
		_, err := Latest(ctx, serve(t, 200, b))
		assert.Error(t, err, name)
	}
}
