// Package runrecord is how Coral says, for the updater, how it was started (#5339): its own command line and working directory, written by the
// server at startup into <data dir>/updates/run.json (0600 in a 0700 folder) and read by coral-update's Linux apply to start Coral again after
// the swap. It is the server's own record, never a value from a request. It has no dependency on the verifier, so the server may link it.
package runrecord

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// File is the record's name in <data dir>/updates.
const File = "run.json"

// Record is what is kept.
type Record struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

const maxSize = 64 << 10

// Write records argv and the working directory for the data folder, atomically, 0600.
func Write(dataDir string, argv []string, cwd string) error {
	if len(argv) == 0 {
		return errors.New("no command line")
	}
	dir := filepath.Join(dataDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(Record{Argv: argv, Cwd: cwd})
	if err != nil {
		return err
	}
	if len(b) > maxSize {
		return errors.New("the command line is too long to record")
	}
	tmp, err := os.CreateTemp(dir, ".run-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, File))
}
