package tracking

import (
	"path/filepath"
	"testing"

	"github.com/subgentic/coral/internal/linktest"
)

// #3920: the delivery-failure log is never written through a link planted at its name (a
// symbolic link; on Windows also a junction).
func TestDeliveryFailureLogNeverFollowsAPlantedLink(t *testing.T) {
	dir := t.TempDir()
	restore := ConfigureForTest("", dir, "test-install")
	defer restore()
	linktest.Each(t, filepath.Join(dir, "tracking-failures.log"), func(t *testing.T, p linktest.Planted) {
		logDeliveryFailure("test_event", 500, "through a link")
	})
}
