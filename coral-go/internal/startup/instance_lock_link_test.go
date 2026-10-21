package startup

import (
	"path/filepath"
	"testing"

	"github.com/subgentic/coral/internal/linktest"
)

// #3920: the instance lock is never taken through a link planted at its name (a symbolic link;
// on Windows also a junction), so the lock can't truncate another file.
func TestInstanceLockNeverFollowsAPlantedLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral-server.lock")
	linktest.Each(t, path, func(t *testing.T, p linktest.Planted) {
		if lock, err := acquireInstanceLock(path); err == nil {
			lock.close(path)
			t.Errorf("%s: the lock was taken through the link", p.Kind)
		}
	})
}
