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

func tag(s string) string { b, _ := json.Marshal(map[string]string{"tag_name": s}); return string(b) }

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
