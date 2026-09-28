package startup

import (
	"path/filepath"
	"testing"
)

func TestInstanceLockIsExclusiveAndReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral-server.lock")
	first, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstanceLock(path); err == nil {
		t.Fatal("second server unexpectedly acquired the instance lock")
	}
	first.close(path)
	second, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	second.close(path)
}
