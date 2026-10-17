package startup

import (
	"log"
	"path/filepath"
	"time"
)

// slowOpenAfter is how long opening a database may take before the log says why startup is
// waiting (a test seam).
var slowOpenAfter = 5 * time.Second

// noticeSlowOpen logs once if opening the database at path is still going after
// slowOpenAfter (#4151): another process holding it locked makes every statement wait out
// the busy timeout, and the server neither answers nor says why. Call the returned func when
// the open returns.
func noticeSlowOpen(path string) (done func()) {
	start := time.Now()
	t := time.AfterFunc(slowOpenAfter, func() {
		log.Printf("[STARTUP] still opening %s after %s: another process may be holding it locked (a backup or sync tool, sqlite3, or another Coral); waiting for it", filepath.Base(path), slowOpenAfter)
	})
	return func() {
		if !t.Stop() {
			log.Printf("[STARTUP] %s opened after %s", filepath.Base(path), time.Since(start).Round(100*time.Millisecond))
		}
	}
}
