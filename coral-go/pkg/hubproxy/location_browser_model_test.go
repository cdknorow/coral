package hubproxy

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// #5086 review: an oracle that is not the filter's own rule. leavesTheOrigin is how a browser reads a redirect's Location against the hub's own
// (special-scheme, http or https) URL, by the URL standard's rules that matter here: a tab, newline or carriage return anywhere is dropped; leading
// and trailing C0 controls and spaces are trimmed; anything with a scheme is another URL; in a special scheme a backslash is a slash; and two
// leading slashes are a network-path reference, which names another host.
var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

func leavesTheOrigin(loc string) bool {
	s := strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(loc)
	s = strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
	if urlScheme.MatchString(s) {
		return true
	}
	return strings.HasPrefix(strings.ReplaceAll(s, "\\", "/"), "//")
}

// Mutation: weaken sameOriginLocation (drop the backslash scan, or the second-byte check): a survivor leaves the origin in this model.
func FuzzStrictResponseFilterLocationLikeABrowser(f *testing.F) {
	for _, s := range []string{"/x", "/\\evil.example", "//evil.example", "https://e/", "/\tx", "/\t/evil.example", "/\n/evil.example", "\\\\x", "/%5Cx", " //x", "/\r/x", "javascript:x", "/a/../../x", "/\\\\x", "/\u00a0/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, loc string) {
		resp := &http.Response{Header: http.Header{"Location": {loc}}}
		if err := StrictResponseFilter(resp); err != nil {
			t.Fatal(err)
		}
		for _, got := range resp.Header.Values("Location") {
			if leavesTheOrigin(got) {
				t.Fatalf("Location %q survived as %q, which a browser reads as another origin", loc, got)
			}
		}
	})
}
