// Package runrecord is how Coral says, for the updater, how it was started (#5339): its own command line and working directory, written by the
// server at startup into <data dir>/updates/run.json (0600 in a 0700 folder) and read by coral-update's Linux apply to start Coral again after
// the swap. It is the server's own record, never a value from a request. It has no dependency on the verifier, so the server may link it.
package runrecord

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// File is the record's name in <data dir>/updates.
const File = "run.json"

// Record is what is kept.
type Record struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

const maxSize = 64 << 10

// ErrSecretFlag is returned for a command line with a flag that carries a secret: it is not recorded (the record is a file; a restart without
// the record is refused, not done without the flag). Start Coral with the secret in its environment or a file instead.
var ErrSecretFlag = errors.New("the command line carries a secret in a flag, so it is not recorded")

var secretFlag = regexp.MustCompile(`(?i)^--?[a-z0-9_-]*(token|secret|password|passwd|api[-_]?key|credential|private[-_]?key)[a-z0-9_-]*(=|$)`)

// Write records argv and the working directory for the data folder, atomically, 0600. A flag named for a secret (--token, --api-key=...,
// --password) makes it refuse: the record must not hold one (Security's #5341 LOW 1).
func Write(dataDir string, argv []string, cwd string) error {
	if len(argv) == 0 {
		return errors.New("no command line")
	}
	for _, a := range argv[1:] {
		if secretFlag.MatchString(a) {
			return ErrSecretFlag
		}
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
