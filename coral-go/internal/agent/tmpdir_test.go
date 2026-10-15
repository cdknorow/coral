package agent

import (
	"os"
	"testing"
)

// TestMain gives the package's tests a temp folder of their own: launch lines write
// prompt files and agy's hooks folder (#3481) there, and it is removed afterwards
// instead of leaving them in the system temp folder.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "coral-agent-test-")
	if err == nil {
		os.Setenv("TMPDIR", dir)
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
