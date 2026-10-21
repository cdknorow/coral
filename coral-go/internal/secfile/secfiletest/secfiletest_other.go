//go:build !windows

package secfiletest

import (
	"os"
	"testing"
)

// AssertPrivate fails the test unless path has mode 0600 (on Windows: an explicit, protected
// access list).
func AssertPrivate(t *testing.T, path string) {
	t.Helper()
	assertMode(t, path)
}

// AssertOwnerOnly fails the test unless path has mode 0600 (on Windows: only this user can open
// it, its access list its own or inherited from a private folder).
func AssertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	assertMode(t, path)
}

// AssertOwnerOnlyAccess fails the test unless path has mode 0600 (on Windows: AssertOwnerOnly's
// access-list rule alone, for a file stored as a hard link).
func AssertOwnerOnlyAccess(t *testing.T, path string) {
	t.Helper()
	assertMode(t, path)
}

func assertMode(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s: mode %o, want 600", path, got)
	}
}

// Expose lets another account read path: mode 0644 (on Windows: the built-in Users group).
func Expose(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// OwnerOnlyDir makes dir private to this user: mode 0700 (on Windows, a protected access list).
func OwnerOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}
