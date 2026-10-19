package hubproxy

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A connector's Location leaves only as a path on the hub's own origin (#5086): slash-backslash is read by a browser as two slashes, so it is as foreign as
// a URL with a host; a second Location header gets no pass either.
func TestStrictResponseFilterKeepsOnlySameOriginLocations(t *testing.T) {
	kept := []string{"/api/remote/x/api/sessions", "/x", "/a/b?c=d", "/a#frag", "/"}
	refused := []string{
		"/\\evil.example/x", "/\\/evil.example", "\\evil.example", "//evil.example/x", "///evil.example", "https://evil.example/", "http://localhost/x",
		"evil.example/x", "", "/a\\b", "/ /evil.example", "/\t/evil.example", "/\n/evil.example", "/\x7f", "javascript:alert(1)", "/%5Cevil.example/x/\\",
	}
	for _, loc := range kept {
		resp := &http.Response{Header: http.Header{"Location": {loc}}}
		assert.NoError(t, StrictResponseFilter(resp))
		assert.Equal(t, loc, resp.Header.Get("Location"), "kept %q", loc)
	}
	for _, loc := range refused {
		resp := &http.Response{Header: http.Header{"Location": {loc}}}
		assert.NoError(t, StrictResponseFilter(resp))
		assert.Empty(t, resp.Header.Values("Location"), "refused %q", loc)
	}
	resp := &http.Response{Header: http.Header{"Location": {"/fine", "/\\evil.example"}}}
	assert.NoError(t, StrictResponseFilter(resp))
	assert.Empty(t, resp.Header.Values("Location"), "a second Location header is checked too")
}

func FuzzStrictResponseFilterLocation(f *testing.F) {
	for _, s := range []string{"/x", "/\\evil.example", "//evil.example", "https://e/", "/a\tb", "\\\\x", "/%5Cx"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, loc string) {
		resp := &http.Response{Header: http.Header{"Location": {loc}}}
		if err := StrictResponseFilter(resp); err != nil {
			t.Fatal(err)
		}
		for _, got := range resp.Header.Values("Location") {
			if !sameOriginLocation(got) || got[0] != '/' || len(got) > 1 && (got[1] == '/' || got[1] == '\\') {
				t.Fatalf("Location %q survived as %q", loc, got)
			}
			for i := 0; i < len(got); i++ {
				if got[i] <= 0x20 || got[i] == 0x7f || got[i] == '\\' {
					t.Fatalf("Location %q survived with byte %#x", loc, got[i])
				}
			}
		}
	})
}

// A Content-Disposition filename is only letters, digits and ". _ -", at most 100 bytes, never a hidden or path-like name.
func FuzzDispositionFilename(f *testing.F) {
	for _, s := range []string{`attachment; filename="a.png"`, `attachment; filename="../../etc/passwd"`, `inline; filename*=UTF-8''%2e%2e%2fx`, `x; filename="..\\a"`, ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cd string) {
		name := dispositionFilename(cd)
		if len(name) > 100*4 {
			t.Fatalf("a filename of %d bytes", len(name))
		}
		if name != "" && name[0] == '.' {
			t.Fatalf("a hidden name %q", name)
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				t.Fatalf("filename %q from %q holds %q", name, cd, c)
			}
		}
	})
}

// A query with an api_key never goes on with it, whatever else is in the query, and one that cannot be read goes on empty.
func FuzzStripAPIKey(f *testing.F) {
	for _, s := range []string{"a=1&api_key=SECRETVALUE&b=2", "api%5Fkey=SECRETVALUE", "x=1;api_key=SECRETVALUE", "api_key=SECRETVALUE&api_key=SECRETVALUE", "%zz&api_key=SECRETVALUE"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		out := stripAPIKey(q)
		if len(q) > 0 && len(out) > 3*len(q)+8 {
			t.Fatalf("%q grew to %d bytes", q, len(out))
		}
		if out == "" {
			return
		}
		vals, err := url.ParseQuery(out)
		if err == nil {
			if _, ok := vals["api_key"]; ok {
				t.Fatalf("%q still has api_key in %q", q, out)
			}
		}
	})
}
