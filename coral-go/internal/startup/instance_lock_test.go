package startup

import (
	"os"
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

// #4151: closing leaves the lock file in place (a server started meanwhile may hold the lock
// on it), and a file left behind never blocks the next server: only the lock does.
func TestInstanceLockFileStaysAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral-server.lock")
	first, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	first.close(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock file was removed: %v", err)
	}
	second, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("a leftover file blocked the next server: %v", err)
	}
	defer second.close(path)
	if _, err := acquireInstanceLock(path); err == nil {
		t.Fatal("the lock no longer keeps a second server out")
	}
}
