package startup

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

// A symlink into a PATH directory is exactly what install-cli.sh creates, so it
// must not be reported as a conflict with itself.
func TestSameFileFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "app", "coral")
	writeExecutable(t, real, "#!/bin/sh\n")

	link := filepath.Join(dir, "bin", "coral")
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if !sameFile(real, link) {
		t.Error("a symlink to our binary must count as the same program, not a conflict")
	}
	if !sameFile(link, real) {
		t.Error("sameFile must be symmetric")
	}
}

// The case this exists for: a different program with the same name.
func TestSameFileDistinguishesADifferentProgram(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "ours", "coral")
	theirs := filepath.Join(dir, "theirs", "coral")
	writeExecutable(t, ours, "#!/bin/sh\necho ours\n")
	writeExecutable(t, theirs, "#!/bin/sh\necho theirs\n")

	if sameFile(ours, theirs) {
		t.Error("two different files named coral must not be treated as the same program")
	}
}

func TestSameFileHandlesMissingPaths(t *testing.T) {
	dir := t.TempDir()
	if sameFile(filepath.Join(dir, "nope"), filepath.Join(dir, "also-nope")) {
		t.Error("two different missing paths must not compare equal")
	}
}

// End to end through the exported function, with PATH controlled.
func TestCheckPATHShadowing(t *testing.T) {
	t.Run("no coral on PATH is not a warning", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if w := CheckPATHShadowing(); w != nil {
			t.Errorf("expected no warning, got %+v", w)
		}
	})

	t.Run("a different coral earlier on PATH warns", func(t *testing.T) {
		dir := t.TempDir()
		other := filepath.Join(dir, "coral")
		writeExecutable(t, other, "#!/bin/sh\necho not ours\n")
		t.Setenv("PATH", dir)

		w := CheckPATHShadowing()
		if w == nil {
			t.Fatal("expected a warning when a different program owns the name")
		}
		if w.OnPath != resolvePath(other) {
			t.Errorf("OnPath = %q, want %q", w.OnPath, resolvePath(other))
		}
		if w.Running == "" || w.Running == w.OnPath {
			t.Errorf("Running should name this test binary and differ from OnPath, got %q", w.Running)
		}
	})

	t.Run("a symlink to this very binary is not a warning", func(t *testing.T) {
		self, err := os.Executable()
		if err != nil {
			t.Skip("cannot resolve the test executable")
		}
		dir := t.TempDir()
		if err := os.Symlink(self, filepath.Join(dir, "coral")); err != nil {
			t.Skip("cannot symlink here")
		}
		t.Setenv("PATH", dir)

		if w := CheckPATHShadowing(); w != nil {
			t.Errorf("a symlink to ourselves must not warn, got %+v", w)
		}
	})
}
