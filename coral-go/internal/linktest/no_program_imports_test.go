package linktest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testOnly are packages that exist for tests and are ordinary (non-_test) packages only so that
// several packages' tests can share them. No program may link one: linktest plants links,
// testleak imports goleak, and secfiletest rewrites access lists and points TMP/TEMP elsewhere
// (Security's PR #53 review).
var testOnly = []string{
	"github.com/subgentic/coral/internal/linktest",
	"github.com/subgentic/coral/internal/testleak",
	"github.com/subgentic/coral/internal/secfile/secfiletest",
}

func TestNoProgramImportsTheTestHelpers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over every program")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, tags := range []string{"fts5", "fts5,dev"} {
			// -e: a program that needs cgo (the tray and desktop app) is listed as far as it resolves.
			cmd := exec.Command("go", "list", "-e", "-deps", "-tags", tags, "-f", "{{.ImportPath}}", "./cmd/...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list (%s, %s): %v", goos, tags, err)
			}
			deps := strings.Fields(string(out))
			seen := map[string]bool{}
			for _, d := range deps {
				seen[d] = true
			}
			for _, p := range []string{"github.com/subgentic/coral/cmd/coral", "github.com/subgentic/coral/cmd/coral-board"} {
				if !seen[p] {
					t.Fatalf("go list (%s, %s) did not list %s: the census looked at nothing", goos, tags, p)
				}
			}
			for _, p := range testOnly {
				if seen[p] {
					t.Errorf("a program links %s (%s, %s): it is for tests only", p, goos, tags)
				}
			}
		}
	}
}
