// Package linktest plants links at a path for the tests of Coral's no-follow opens (#3920,
// Security's #3903 item 5): every opener of a file in a folder an agent can write to is checked
// against each kind of link the platform has, with this one helper.
package linktest

import (
	"os"
	"path/filepath"
	"testing"
)

// Sentinel is what a planted link's target file holds; a no-follow open must leave it as it is.
const Sentinel = "the target of a planted link\n"

// Planted is one link planted at a path: its kind and what it points to (a file, or for a
// junction a folder).
type Planted struct {
	Kind   string
	Target string
	Folder bool
}

// Each plants, one at a time, every kind of link this platform has at path (a symbolic link to
// a file, a hard link to a file; on Windows also a junction to a folder), runs fn, then checks
// the target is unchanged and removes the link and its target. path must not exist.
func Each(t *testing.T, path string, fn func(t *testing.T, p Planted)) {
	t.Helper()
	for _, kind := range kinds() {
		t.Run(kind, func(t *testing.T) {
			p := plant(t, kind, path)
			defer func() {
				_ = os.Remove(path)
				_ = os.RemoveAll(p.Target)
			}()
			fn(t, p)
			p.Unchanged(t)
		})
	}
}

// Unchanged fails the test if the link's target was written to: the file must still hold
// Sentinel, the folder must still be empty.
func (p Planted) Unchanged(t *testing.T) {
	t.Helper()
	if p.Folder {
		entries, err := os.ReadDir(p.Target)
		if err != nil {
			t.Fatalf("%s: read the target folder: %v", p.Kind, err)
		}
		if len(entries) != 0 {
			t.Errorf("%s: something was written through the link into %s", p.Kind, p.Target)
		}
		return
	}
	b, err := os.ReadFile(p.Target)
	if err != nil {
		t.Fatalf("%s: read the target: %v", p.Kind, err)
	}
	if string(b) != Sentinel {
		t.Errorf("%s: the target was changed through the link: %q", p.Kind, b)
	}
}

func plantSymlink(t *testing.T, path string) Planted {
	t.Helper()
	target := path + ".symlink-target"
	if err := os.WriteFile(target, []byte(Sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	Symlink(t, target, path)
	return Planted{Kind: "symlink", Target: target}
}

// Symlink makes link a symbolic link to target, or skips the test where this account can't
// (Windows without Developer Mode or an elevated user: "A required privilege is not held by the
// client"). On CI it fails instead: the runner must allow links, so the check always runs there.
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("create a symbolic link: %v (the CI runner must allow it, so the check runs)", err)
		}
		t.Skipf("can't create a symbolic link here (Windows needs Developer Mode or an elevated user): %v", err)
	}
}

// RequireSymlinks skips the test (fails it on CI) where this account can't create symbolic links,
// for a test whose links are made by something else (a script's ln -s), which would otherwise
// quietly make a plain file and leave the test checking nothing.
func RequireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	Symlink(t, dir, filepath.Join(dir, "probe"))
}

// plantHardLink gives a file elsewhere a second name at path. Reading it is harmless (the same
// user could read the file), but a write through it changes the other file. Hard links need no
// privilege on any platform, so this kind never skips.
func plantHardLink(t *testing.T, path string) Planted {
	t.Helper()
	target := path + ".hardlink-target"
	if err := os.WriteFile(target, []byte(Sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, path); err != nil {
		t.Fatalf("plant a hard link: %v", err)
	}
	return Planted{Kind: "hardlink", Target: target}
}
