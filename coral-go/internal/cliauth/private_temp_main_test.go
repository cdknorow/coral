package cliauth

import (
	"os"
	"testing"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// TestMain makes the package's secret files in a private temp folder, so a foreign entry on the
// real %TEMP% (Codex's sandbox adds one) can't fail its private-file checks (#28).
func TestMain(m *testing.M) {
	done := secfiletest.UsePrivateTemp()
	code := m.Run()
	done()
	os.Exit(code)
}
