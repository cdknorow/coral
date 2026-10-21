package auth

import (
	"os"
	"testing"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
	"github.com/subgentic/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167). Its secret files
// are made in a private temp folder, so a foreign entry on the real %TEMP% can't fail them (#28).
func TestMain(m *testing.M) {
	done := secfiletest.UsePrivateTemp()
	code := testleak.Check(m.Run())
	done()
	os.Exit(code)
}
