package routes

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A "person or tool at this computer" decision must go through auth.IsDirectLocal, not a bare
// RemoteAddr loopback check in a route handler: the latter grants its local-only power to a
// request a same-machine reverse proxy forwarded, and can't see require_auth_on_loopback (#3585,
// Security's MUST 1). This test fails if any function in this package derives an IP from
// r.RemoteAddr and calls IsLoopback on it, so the next copy fails the build.
func TestNoRemoteAddrLoopbackCheckInRoutes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcSplit := regexp.MustCompile(`(?m)^func\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Check per function: a RemoteAddr loopback check is RemoteAddr and IsLoopback in the same
		// body. (IsLoopback on an interface address, with no RemoteAddr, is a different thing and
		// stays allowed.)
		for _, fn := range funcSplit.Split(string(data), -1) {
			if strings.Contains(fn, "RemoteAddr") && strings.Contains(fn, "IsLoopback") {
				name := "a function"
				if i := strings.IndexByte(fn, '('); i > 0 {
					name = strings.TrimSpace(fn[:i])
				}
				t.Errorf("%s: %s derives loopback from r.RemoteAddr; use auth.IsDirectLocal(r) instead (#3585)", f, name)
			}
		}
	}
}
