package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// EnsureDataDir creates Coral's data folder if it doesn't exist, readable by this user only:
// 0700 on Unix; on Windows, where a mode does nothing, a protected DACL that grants the
// current user and SYSTEM and nobody else, inherited by everything Coral writes in it
// (#3637, Security's SHOULD on #3631). An existing folder is left as it is; DataDirBroadReaders
// reports one that others can read.
func EnsureDataDir(dir string) error {
	if dir == "" {
		return errors.New("no data folder")
	}
	_, err := os.Lstat(dir)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !existed {
		return protectDataDir(dir)
	}
	return nil
}

// DataDirWarning is the warning for a data folder other people on this computer can read, or ""
// when there is none to give (Windows only: on Unix the server's permission pass tightens the
// mode instead).
func DataDirWarning(dir string) string {
	groups, err := DataDirBroadReaders(dir)
	if err != nil {
		return fmt.Sprintf("couldn't read the data folder's access list (%s): %v", dir, err)
	}
	if len(groups) == 0 {
		return ""
	}
	return fmt.Sprintf("WARNING: Coral's data folder %s can be read by %s. It holds the API key, the command-line token and the databases. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security).",
		dir, strings.Join(groups, ", "))
}
