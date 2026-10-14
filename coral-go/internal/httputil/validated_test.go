package httputil

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validatedGet(t *testing.T, v any, ifNoneMatch, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	w := httptest.NewRecorder()
	WriteJSONValidated(w, r, http.StatusOK, v)
	return w
}

// #3439: 200 with a weak ETag and no-cache; the same answer again is a 304 with no body; a changed
// answer (a different capture, or the same text at another size) gets a new ETag and a 200.
func TestWriteJSONValidated(t *testing.T) {
	first := validatedGet(t, map[string]any{"capture": "hello"}, "", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	assert.True(t, strings.HasPrefix(etag, `W/"`), etag)
	assert.Equal(t, "no-cache", first.Header().Get("Cache-Control"))
	assert.Equal(t, etag, first.Header().Get(ValidatorHeader), "the same validator, for the hub (which drops ETag)")
	assert.JSONEq(t, `{"capture":"hello"}`, first.Body.String())

	same := validatedGet(t, map[string]any{"capture": "hello"}, etag, "")
	assert.Equal(t, http.StatusNotModified, same.Code)
	assert.Empty(t, same.Body.String(), "no body on 304")
	assert.Equal(t, etag, same.Header().Get("ETag"))

	changed := validatedGet(t, map[string]any{"capture": "hello!"}, etag, "")
	assert.Equal(t, http.StatusOK, changed.Code)
	assert.NotEqual(t, etag, changed.Header().Get("ETag"))
	longer := validatedGet(t, map[string]any{"capture": "hello", "pad": strings.Repeat(" ", 3)}, etag, "")
	assert.Equal(t, http.StatusOK, longer.Code, "another size, another validator")

	// A gzipped answer has the same (weak) validator, and a 304 is a 304 either way.
	big := map[string]any{"capture": strings.Repeat("line of output\n", 2000)}
	gz := validatedGet(t, big, "", "gzip")
	plain := validatedGet(t, big, "", "")
	assert.Equal(t, "gzip", gz.Header().Get("Content-Encoding"))
	assert.Equal(t, plain.Header().Get("ETag"), gz.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, validatedGet(t, big, plain.Header().Get("ETag"), "gzip").Code)
}

func TestETagMatches(t *testing.T) {
	assert.True(t, ETagMatches(`W/"abc"`, `W/"abc"`))
	assert.True(t, ETagMatches(`"abc"`, `W/"abc"`), "weak comparison")
	assert.True(t, ETagMatches(`"x", W/"abc"`, `W/"abc"`), "a list")
	assert.True(t, ETagMatches(`*`, `W/"abc"`))
	assert.False(t, ETagMatches(`W/"abd"`, `W/"abc"`))
	assert.False(t, ETagMatches(``, `W/"abc"`))
}

// coral-hub's rule for a validator it relays (X-Coral-Validator): an opaque token derived from the
// content only, at most 128 bytes of printable ASCII, carrying nothing that isn't in the body.
// Ours is W/"<24 hex of SHA-256 of the JSON>".
func TestValidatorIsAnOpaqueShortToken(t *testing.T) {
	format := regexp.MustCompile(`^W/"[0-9a-f]{24}"$`)
	for _, v := range []any{
		map[string]any{},
		map[string]any{"capture": strings.Repeat("x", 100000), "path": "/Users/someone/secret/repo"},
		[]any{map[string]any{"name": "agent", "working_directory": "/home/u/proj"}},
	} {
		w := validatedGet(t, v, "", "")
		tag := w.Header().Get(ValidatorHeader)
		assert.Regexp(t, format, tag)
		assert.LessOrEqual(t, len(tag), 128)
		for _, c := range tag {
			assert.True(t, c >= 0x21 && c <= 0x7e, "printable ASCII: %q", c)
		}
		assert.NotContains(t, tag, "/Users", "nothing from the body in clear")
		assert.Equal(t, w.Header().Get("ETag"), tag, "the same as the ETag")
	}
}
