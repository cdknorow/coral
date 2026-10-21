package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// EnsureDataDir creates Coral's data folder if it doesn't exist, readable by this user only:
// 0700 on Unix; on Windows, where a mode does nothing, a protected DACL that grants the
// current user and SYSTEM and nobody else, inherited by everything Coral writes in it
// (#3637, Security's SHOULD on #3631). An existing folder is left as it is; DataDirBroadReaders
// reports one that others can read.
//
// On Windows an EXISTING folder that other accounts can read (a CORAL_DATA_DIR such as D:\coral inherits Users:(RX) from the
// drive root, which exposes the databases, the API key and the tokens) is tightened when this user owns it: it gets that same
// protected owner-only list and its existing files and folders are re-protected (links and junctions are never followed). One
// this user does not own, or whose list cannot be changed, stops Coral from starting (ErrSharedDataDir) unless
// --allow-shared-data-dir (CORAL_ALLOW_SHARED_DATA_DIR=1) says to run on it anyway.
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
	notice, err := secureExistingDataDir(dir)
	if err != nil {
		if AllowSharedDataDir() {
			log.Printf("[security] %v (continuing: --allow-shared-data-dir)", err)
			setDataDirNotice(fmt.Sprintf("Coral is running on a data folder other accounts can read, as --allow-shared-data-dir says: %v", err))
			return nil
		}
		return err
	}
	if notice != "" {
		log.Printf("[security] %s", notice)
		setDataDirNotice(notice)
	}
	return nil
}

// ErrSharedDataDir is the error of an existing data folder that other accounts can read and that Coral could not make private.
var ErrSharedDataDir = errors.New("the data folder can be read by other accounts")

var (
	allowSharedDataDir atomic.Bool
	dataDirNoticeMu    sync.Mutex
	dataDirNotice      string
)

// SetAllowSharedDataDir is --allow-shared-data-dir: run on a data folder other accounts can read even when it can't be made private.
func SetAllowSharedDataDir(allow bool) { allowSharedDataDir.Store(allow) }

// AllowSharedDataDir reports --allow-shared-data-dir or CORAL_ALLOW_SHARED_DATA_DIR.
func AllowSharedDataDir() bool {
	return allowSharedDataDir.Load() || Truthy(os.Getenv("CORAL_ALLOW_SHARED_DATA_DIR"))
}

func setDataDirNotice(s string) {
	dataDirNoticeMu.Lock()
	dataDirNotice = s
	dataDirNoticeMu.Unlock()
}

func getDataDirNotice() string {
	dataDirNoticeMu.Lock()
	defer dataDirNoticeMu.Unlock()
	return dataDirNotice
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
		// Nothing to warn about now; say what Coral did about it at startup, if it did something.
		return getDataDirNotice()
	}
	return fmt.Sprintf("WARNING: Coral's data folder %s can be read by %s. It holds the API key, the command-line token and the databases. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security).",
		dir, strings.Join(groups, ", "))
}
