package startup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/config"
)

// strictNoticeFile marks a data folder whose log has said once that strict local auth is the
// default (#3936).
const strictNoticeFile = "strict_local_auth_notice"

// strictLocalAuth decides strict local auth for this run (#3936): the command line's choice if
// it made one, otherwise on unless CORAL_STRICT_LOCAL_AUTH says off (a dev build only, #4290). The first run on a data
// folder logs once how to sign in and how to turn it off; off is logged on every start.
func strictLocalAuth(dataDir string, override *bool) (bool, error) {
	on, warning := config.StrictLocalAuthFromEnv()
	if warning != "" {
		log.Print(warning)
	}
	if override != nil {
		on = *override
	}
	if !on {
		if err := refuseStrictOffOnDefaultDir(false, dataDir); err != nil {
			return false, err
		}
		log.Printf("[auth] WARNING: strict local auth is OFF (--strict-local-auth=false, or CORAL_STRICT_LOCAL_AUTH=0 in a dev build): any program on this computer can use Coral without a credential")
		return false, nil
	}
	marker := filepath.Join(dataDir, strictNoticeFile)
	if _, err := os.Lstat(marker); err == nil {
		return true, nil
	}
	log.Printf("[auth] Coral now requires a credential for every request, including from this computer. " +
		"Open the dashboard from the app, the tray or 'coral open'; a script sends the API key " +
		"('Authorization: Bearer' with the key 'coral api-key' prints). " +
		"--strict-local-auth=false turns this off, which is weaker. This message is shown once.")
	if err := os.WriteFile(marker, []byte("shown\n"), 0o600); err != nil {
		log.Printf("[auth] couldn't record that the notice was shown: %v", err)
	}
	return true, nil
}

// refuseStrictOffOnDefaultDir stops a start that has strict local auth off on the default data
// folder (~/.coral), where the operator's real sessions and keys live: off is for a test server on
// its own CORAL_DATA_DIR (#4290, Security's audit #4288). A folder that resolves to the default
// through a symlink, a trailing slash or CORAL_DATA_DIR=~/.coral is the default too.
func refuseStrictOffOnDefaultDir(strict bool, dataDir string) error {
	if strict {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil // no default to compare with
	}
	if !sameDir(dataDir, filepath.Join(home, ".coral")) {
		return nil
	}
	return fmt.Errorf("strict local auth is off and the data folder is the default %s: any program on this computer could use Coral without a credential, with your real sessions and keys. Set CORAL_DATA_DIR (or --home) to a folder of its own for a test server, or turn strict local auth back on", dataDir)
}

// sameDir reports whether a and b name the same folder. Two folders that exist are compared as the
// file system does (os.SameFile), so a different case on a case-insensitive volume (macOS, Windows),
// an 8.3 short name or a symlink can't hide the default folder; when one does not exist the paths
// are compared as strings (cleaned, absolute, symlinks resolved).
func sameDir(a, b string) bool {
	if ia, err := os.Stat(a); err == nil {
		if ib, err := os.Stat(b); err == nil {
			return os.SameFile(ia, ib)
		}
	}
	norm := func(p string) string {
		p = filepath.Clean(p)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return p
	}
	return norm(a) == norm(b)
}
