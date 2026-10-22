package startup

import (
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"os"
	"path/filepath"
)

// orphanProxyCAFiles are what the removed MITM proxy (gone in 8997a45f) left in the data folder
// that nothing reads now: its CA private key and certificate. proxy-ca-bundle.pem (the system
// roots plus that CA) stays for now: a long-lived Codex pane started before v1.3.14 may still have
// SSL_CERT_FILE pointing at it, and removing it would break that agent's TLS. It goes two releases
// after this one (Security's #4381 review, option a).
var orphanProxyCAFiles = []string{"proxy-ca-key.pem", "proxy-ca.pem"}

// removeOrphanProxyCA deletes those files from dataDir (#4381, Security's #4371 SHOULD 3): by their
// exact names, and only regular files (a symlink or a folder by that name is left alone). It
// returns the names it removed, each also logged.
func removeOrphanProxyCA(dataDir string) []string {
	var removed []string
	for _, name := range orphanProxyCAFiles {
		path := filepath.Join(dataDir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(path); err != nil {
			log.Printf("[startup] could not remove the old proxy CA file %s: %v", logsafe.Path(path), err)
			continue
		}
		log.Printf("[startup] removed the old proxy CA file %s (left by the removed cost proxy; if you added that CA to a browser or keychain by hand, remove it there too)", logsafe.Path(path))
		removed = append(removed, name)
	}
	return removed
}
