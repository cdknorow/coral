package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Agent pipe logs live under the data directory, never in the shared system temp dir: for an
// explicit data directory (#3259) and, by the same rule with no special case, for the default
// ~/.coral install (#3601, weak site W17). The default install isn't loaded here: the test guard
// refuses a test that resolves a real ~/.coral.
func TestLogDirIsUnderTheDataDir(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("CORAL_DATA_DIR", data)
	t.Setenv("CORAL_DIR", "")
	want := filepath.Join(data, "logs")
	if got := Load().LogDir; got != want {
		t.Fatalf("CORAL_DATA_DIR LogDir = %q, want %q", got, want)
	}

	t.Setenv("CORAL_DATA_DIR", "")
	flagDir := filepath.Join(t.TempDir(), "flag")
	if got := Load(flagDir).LogDir; got != filepath.Join(flagDir, "logs") {
		t.Fatalf("--home LogDir = %q", got)
	}
	if got := Load(flagDir).LogDir; got == os.TempDir() {
		t.Fatalf("LogDir is the shared temp dir %q", got)
	}
}
