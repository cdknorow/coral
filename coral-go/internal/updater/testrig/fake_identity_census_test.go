package testrig

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6290 (Security's condition): the rehearsal's fake identities (FakeIssuer, FakeRepo, ...: "rehearsal.invalid", an issuer that cannot exist) are for tests only and must never be in a program Coral ships.
// Two checks here, and a third on the built binaries in the release scripts (tests/release/verify_standard_bundle.*, verify-release.sh: a binary holding "rehearsal.invalid" fails):
//  1. no program under cmd/ links this package (go list -deps), so the constants and the fake signer cannot be in any release binary;
//  2. no Go file outside this package and outside tests names the fake issuer or repository, so a copy of the constant cannot slip into shipped code either.

// fakeMarker is the part of every fake identity a binary can be searched for: the issuer and the repository both carry it.
const fakeMarker = "rehearsal.invalid"

func TestTheFakeIdentitiesLookLikeWhatTheCensusSearchesFor(t *testing.T) {
	assert.Contains(t, FakeIssuer, fakeMarker)
	assert.Contains(t, FakeRepo, fakeMarker)
	assert.Contains(t, FakePromoteRepo, fakeMarker)
}

func TestNoProgramLinksTheRehearsalPackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on the path")
	}
	root := coralGoRoot(t)
	for _, tags := range []string{"", "dev", "webview", "dev,webview"} {
		args := []string{"list", "-deps", "-f", "{{.ImportPath}}"}
		if tags != "" {
			args = append(args, "-tags", tags)
		}
		args = append(args, "./cmd/...")
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil && tags == "webview" || err != nil && tags == "dev,webview" {
			continue // the webview programs need cgo and a toolchain a runner may not have: the plain lists cover every other program
		}
		require.NoError(t, err, "go list (tags %q): %s", tags, out)
		for _, pkg := range strings.Fields(string(out)) {
			assert.False(t, strings.HasSuffix(pkg, "/internal/updater/testrig"), "a program links the rehearsal package (tags %q): its fake identities would be in the binary", tags)
		}
	}
}

func TestNoShippedGoFileNamesAFakeIdentity(t *testing.T) {
	root := coralGoRoot(t)
	var bad []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", "vendor", ".git", "dist":
				return filepath.SkipDir
			case "testrig":
				if filepath.Base(filepath.Dir(p)) == "updater" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), fakeMarker) {
			rel, _ := filepath.Rel(root, p)
			bad = append(bad, filepath.ToSlash(rel))
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, bad, "shipped code names the rehearsal's fake identity (only internal/updater/testrig and tests may)")
}

// coralGoRoot is the coral-go folder (the module root), found from this package's directory.
func coralGoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "go.mod not found at %s", root)
	return root
}
