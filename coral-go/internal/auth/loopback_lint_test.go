package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// isLocalhost is the raw IP-only check; a trust decision must go through isDirectLocal, which
// also rejects a request a local reverse proxy forwarded (#3585). This test fails if a new call
// to isLocalhost appears outside the few places allowed to use the raw check, so a later change
// can't reintroduce the proxy-bypass by calling isLocalhost directly for a trust decision.
func TestIsLocalhostOnlyInAllowedPlaces(t *testing.T) {
	// file -> the exact lines (trimmed) permitted to mention isLocalhost.
	allowed := map[string][]string{
		"keystore.go": {
			"func isLocalhost(r *http.Request) bool {", // the definition
			"if isLocalhost(r) {",                      // fromTrustedProxy: header belief, not a no-credentials grant (see its comment)
		},
		"loopback.go": {
			"func isDirectLocal(r *http.Request) bool {", // the one wrapper
			"if !isLocalhost(r) {",
		},
	}
	call := regexp.MustCompile(`\bisLocalhost\s*\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !call.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			ok := false
			for _, a := range allowed[filepath.Base(f)] {
				if trimmed == a {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s:%d calls isLocalhost for a trust decision; use isDirectLocal (#3585):\n\t%s", f, i+1, trimmed)
			}
		}
	}
}
