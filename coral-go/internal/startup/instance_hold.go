package startup

import (
	"fmt"
	"os"
	"path/filepath"
)

// Instance is a held instance lock of one data folder (the file coral-server.lock in it): one server runs on a data folder at a time.
type Instance struct {
	lock *instanceLock
	path string
}

func lockError(err error) error {
	return fmt.Errorf("cannot start Coral: %w (stop the existing server or use a different data directory)", err)
}

// LockInstance takes the instance lock of coralDir before anything else touches the folder (#6388): config.Load makes the folder and tightens its modes, the log is opened in it and the Terms
// marker is written there, so a second start that loses the lock must lose it before all of that. A folder that does not exist yet has no server to lose to: it returns (nil, nil), and
// Start makes the folder and takes the lock itself. A folder that is a link is not a folder here (Lstat) and gets the same answer: the safe direction, Start's order. Pass the result to Start in
// Options.Instance; Start releases it on any failure and Close releases it, and a caller that exits first releases it by exiting.
func LockInstance(coralDir string) (*Instance, error) {
	fi, err := os.Lstat(coralDir)
	if err != nil || !fi.IsDir() {
		return nil, nil
	}
	path := filepath.Join(coralDir, "coral-server.lock")
	l, err := acquireInstanceLock(path)
	if err != nil {
		return nil, lockError(err)
	}
	return &Instance{lock: l, path: path}, nil
}

// Release gives the lock up. Start does it itself for an Instance it was given; this is for a caller that holds one and never reaches Start, and for tests.
func (i *Instance) Release() {
	if i != nil {
		i.lock.close(i.path)
	}
}
