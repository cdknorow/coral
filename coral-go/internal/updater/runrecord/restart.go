package runrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/subgentic/coral/internal/secfile"
)

// ManagedByService reports that a service manager (systemd) started this process and restarts it by itself: nothing then has to start Coral again, and the record
// is not needed.
func ManagedByService() bool { return os.Getenv("INVOCATION_ID") != "" }

// Validate says whether rr can start the coral of install again: the program is the absolute path of "coral" in that folder (never another program or folder),
// and there is an environment to give it (the allowlisted part, never the updater's own). It is the one rule both the server (before it offers a relaunch) and the
// apply (before it starts and when it restarts) use (#5419).
func Validate(rr Record, install string) error {
	if len(rr.Argv) == 0 || !filepath.IsAbs(rr.Argv[0]) || filepath.Dir(filepath.Clean(rr.Argv[0])) != install || filepath.Base(rr.Argv[0]) != "coral" {
		return errors.New("the recorded command is not coral of this install")
	}
	if len(AllowedEnv(rr.Env)) == 0 {
		return errors.New("the run record has no environment for Coral")
	}
	return nil
}

// Read reads <dataDir>/updates/run.json without following a link: a regular file, owner-only where modes exist, at most maxSize.
func Read(dataDir string) (Record, error) {
	path := filepath.Join(dataDir, "updates", File)
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return Record{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSize || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0) {
		return Record{}, errors.New("the run record is not a private regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return Record{}, err
	}
	var rr Record
	if err := json.Unmarshal(b, &rr); err != nil {
		return Record{}, err
	}
	return rr, nil
}

// Restartable says whether Coral can be started again once it has ended, for the coral program exe that is running now: always off Linux (the other platforms' applies
// start the app their own way), always under a service manager, and otherwise when the record in dataDir is one Validate accepts for exe's folder. A nil error is "yes".
func Restartable(dataDir, exe string) error {
	if runtime.GOOS != "linux" || ManagedByService() {
		return nil
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("the running program cannot be found: %w", err)
	}
	rr, err := Read(dataDir)
	if err != nil {
		return fmt.Errorf("no usable record of how Coral was started: %w", err)
	}
	return Validate(rr, filepath.Dir(real))
}
