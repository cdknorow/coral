//go:build webview

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// serverLogPath is the log a user should open when the server behind this window stops
// (#4151): a server started by the tray writes tray.log, one started by coral writes
// coral.log, both in its data folder. The one written last is this server's; with neither,
// coral.log. The home folder is shown as "~".
func serverLogPath(dataDir string) string {
	best, bestTime := filepath.Join(dataDir, "coral.log"), int64(-1)
	for _, name := range []string{"tray.log", "coral.log"} {
		p := filepath.Join(dataDir, name)
		if fi, err := os.Stat(p); err == nil && fi.ModTime().UnixNano() > bestTime {
			best, bestTime = p, fi.ModTime().UnixNano()
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, best); err == nil && !strings.HasPrefix(rel, "..") {
			best = filepath.Join("~", rel)
		}
	}
	return best
}

// logHintScript sets window.__CORAL_LOG_PATH__ for the disconnect overlay, as a JSON string
// (the overlay shows it with textContent, never as HTML).
func logHintScript(dataDir string) string {
	b, _ := json.Marshal(serverLogPath(dataDir))
	return "window.__CORAL_LOG_PATH__ = " + string(b) + ";"
}
