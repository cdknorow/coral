package startup

import (
	"log"
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/config"
)

// strictNoticeFile marks a data folder whose log has said once that strict local auth is the
// default (#3936).
const strictNoticeFile = "strict_local_auth_notice"

// strictLocalAuth decides strict local auth for this run (#3936): the command line's choice if
// it made one, otherwise on unless CORAL_STRICT_LOCAL_AUTH says off. The first run on a data
// folder logs once how to sign in and how to turn it off; off is logged on every start.
func strictLocalAuth(dataDir string, override *bool) bool {
	on, warning := config.StrictLocalAuthFromEnv()
	if warning != "" {
		log.Print(warning)
	}
	if override != nil {
		on = *override
	}
	if !on {
		log.Printf("[auth] WARNING: strict local auth is OFF (CORAL_STRICT_LOCAL_AUTH=0 or --strict-local-auth=false): any program on this computer can use Coral without a credential")
		return false
	}
	marker := filepath.Join(dataDir, strictNoticeFile)
	if _, err := os.Lstat(marker); err == nil {
		return true
	}
	log.Printf("[auth] Coral now requires a credential for every request, including from this computer. " +
		"Open the dashboard from the app, the tray or 'coral open'; a script sends the API key " +
		"('Authorization: Bearer' with the key 'coral api-key' prints). " +
		"CORAL_STRICT_LOCAL_AUTH=0 turns this off, which is weaker. This message is shown once.")
	if err := os.WriteFile(marker, []byte("shown\n"), 0o600); err != nil {
		log.Printf("[auth] couldn't record that the notice was shown: %v", err)
	}
	return true
}
