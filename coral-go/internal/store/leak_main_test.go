package store

import (
	"os"
	"testing"

	"github.com/cdknorow/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167).
func TestMain(m *testing.M) {
	os.Exit(testleak.Check(m.Run()))
}
