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
		setDataDirFixed(notice)
	}
	return nil
}

// ErrSharedDataDir is the error of an existing data folder that other accounts can read and that Coral could not make private.
var ErrSharedDataDir = errors.New("the data folder can be read by other accounts")

var (
	allowSharedDataDir atomic.Bool
	dataDirNoticeMu    sync.Mutex
	dataDirNotice      string // a folder that stays open to others (--allow-shared-data-dir): reported with every status, as long as it is
	dataDirFixed       string // what Coral did to make an open folder private: told once
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

func setDataDirFixed(s string) {
	dataDirNoticeMu.Lock()
	dataDirFixed = s
	dataDirNoticeMu.Unlock()
}

// DataDirFixed is what Coral did at startup to make its data folder private, or "" when it did nothing or the dashboard has acknowledged it
// (AckDataDirFixed). A fixed folder is told as a fix until the dashboard has shown it, and never as a warning: DataDirWarning is for a folder
// that is still open (#295). Reading it changes nothing, so a CLI or a second tab that reads the status does not use it up.
func DataDirFixed() string {
	dataDirNoticeMu.Lock()
	defer dataDirNoticeMu.Unlock()
	return dataDirFixed
}

// AckDataDirFixed is the dashboard saying it has shown the notice: it is not in the status after this.
func AckDataDirFixed() {
	dataDirNoticeMu.Lock()
	dataDirFixed = ""
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
		// Nothing is open to others now. What Coral did about it at startup is told by DataDirFixed, not as a warning.
		return getDataDirNotice()
	}
	return fmt.Sprintf("Coral's data folder %s can be read by %s. It holds the API key, the command-line token and the databases. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security).",
		dir, strings.Join(groups, ", "))
}

// coralDataNames are what Coral keeps in its data folder besides its databases (*.db): a folder with any of them is a Coral data folder.
var coralDataNames = map[string]bool{
	"api-key": true, "cli-token": true, "session_tokens": true, "artifacts": true, "uploads": true, "logs": true, "tmp": true,
	"agent_knowledge": true, "workflows": true, "themes": true, "pi-sessions": true, "updates": true,
	// What a folder holds before any database exists: the MSI makes it with only the EULA marker, and the first start writes the lock, the pid
	// and the logs before the first database is created (#5698 review: the folder the installer makes must count as Coral's).
	".eula-accepted": true, "coral-server.lock": true, "tray.pid": true, "coral.log": true, "tray.log": true, "app.log": true, "input-debug.log": true,
}

// looksLikeCoralDataDir says whether an existing folder is Coral's own: it is empty, or it holds a database (*.db) or one of
// coralDataNames. A folder that is none of those (a CORAL_DATA_DIR pointed at a project or a drive) is someone else's data, and Coral does
// not rewrite the permissions of every file in it (#5698 review of #96). other names one entry that is not Coral's, for the message.
func looksLikeCoralDataDir(dir string) (ok bool, other string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, ""
	}
	if len(entries) == 0 {
		return true, ""
	}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if coralDataNames[n] || strings.HasSuffix(n, ".db") || strings.HasSuffix(n, ".db-wal") || strings.HasSuffix(n, ".db-shm") {
			return true, ""
		}
		other = e.Name()
	}
	return false, other
}
