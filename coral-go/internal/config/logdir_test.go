package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A server on an explicit data directory writes agent pipe logs under it, not in the
// shared system temp dir; the default ~/.coral install keeps the temp dir (#3259).
func TestLogDirFollowsAnExplicitDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	t.Setenv("CORAL_DATA_DIR", "")
	t.Setenv("CORAL_DIR", "")
	if got := Load().LogDir; got != os.TempDir() {
		t.Fatalf("default install LogDir = %q, want the temp dir %q", got, os.TempDir())
	}

	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("CORAL_DATA_DIR", data)
	want := filepath.Join(data, "logs")
	if got := Load().LogDir; got != want {
		t.Fatalf("CORAL_DATA_DIR LogDir = %q, want %q", got, want)
	}

	t.Setenv("CORAL_DATA_DIR", "")
	flagDir := filepath.Join(t.TempDir(), "flag")
	if got := Load(flagDir).LogDir; got != filepath.Join(flagDir, "logs") {
		t.Fatalf("--home LogDir = %q", got)
	}
}
