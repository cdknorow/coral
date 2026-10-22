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
	DisableProofForTests() // the package's stand-in servers answer the hello, not the proof; the proof has its own tests (proof_transport_test.go)
	code := m.Run()
	done()
	os.Exit(code)
}
