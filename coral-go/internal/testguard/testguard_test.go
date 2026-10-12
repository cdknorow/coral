package testguard

import (
	"path/filepath"
	"testing"
)

func TestCheckDataPathPanicsOnRealCoralDir(t *testing.T) {
	if len(protectedDirs()) == 0 {
		t.Skip("no home directory to protect")
	}
	real := protectedDirs()[0]
	for _, p := range []string{real, filepath.Join(real, "tmux.sock"), filepath.Join(real, "sub", "..", "sessions.db")} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("CheckDataPath(%q) did not panic", p)
				}
			}()
			CheckDataPath(p)
		}()
	}
}

func TestCheckDataPathAllowsOtherDirs(t *testing.T) {
	CheckDataPath("")
	CheckDataPath(t.TempDir())
	CheckDataPath(filepath.Join(t.TempDir(), ".coral", "tmux.sock"))
	if len(protectedDirs()) > 0 {
		CheckDataPath(protectedDirs()[0] + "-other") // sibling, not inside
	}
}

func TestCheckDataPathIsInertOutsideTests(t *testing.T) {
	if len(protectedDirs()) == 0 {
		t.Skip("no home directory to protect")
	}
	enabled = func() bool { return false }
	t.Cleanup(func() { enabled = testing.Testing })
	CheckDataPath(protectedDirs()[0])
}
