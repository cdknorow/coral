package secfiletest

import (
	"os"
	"runtime"

	"github.com/subgentic/coral/internal/secfile"
)

// UsePrivateTemp, called from a TestMain, gives the test binary a temp folder with a protected,
// owner-only access list and points TMP and TEMP at it (Windows), so every t.TempDir() is made
// inside it. A secret file a test makes then has this user's own access only, whatever the real
// %TEMP% grants: Codex's Windows sandbox adds a Modify entry for its capability SID to every
// writable root it is given (the user's %TEMP% among them), and Coral's private-file checks
// rightly refuse a file that inherits it (#28). The returned func removes the folder. Elsewhere
// it does nothing: Unix files take their mode, not their folder's.
func UsePrivateTemp() (cleanup func()) {
	if runtime.GOOS != "windows" {
		return func() {}
	}
	dir, err := os.MkdirTemp("", "coral-private-test-")
	if err != nil {
		return func() {}
	}
	if err := secfile.Protect(dir); err != nil {
		os.RemoveAll(dir)
		return func() {}
	}
	os.Setenv("TMP", dir)
	os.Setenv("TEMP", dir)
	return func() { os.RemoveAll(dir) }
}
