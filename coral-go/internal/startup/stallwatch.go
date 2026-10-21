package startup

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/stallwatch"
	"github.com/subgentic/coral/internal/store"
)

// startStallWatch runs the stall watchdog (#3262): every 10 s it checks the HTTP router,
// the logger and both databases, and when one hasn't answered for 30 s it writes a full
// goroutine dump to <data>/stall-<time>.txt (direct file writes, at most one per stall
// and per 10 minutes, the newest 5 kept). CORAL_STALL_WATCHDOG=off turns it off.
// It returns the watchdog (nil when off), whose Stats feed the hub's machine health (#3444).
func startStallWatch(ctx context.Context, dataDir string, router http.Handler, db *store.DB, bs *board.Store) *stallwatch.Watchdog {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("CORAL_STALL_WATCHDOG"))); v == "off" || v == "0" || v == "false" {
		log.Printf("[stallwatch] off (CORAL_STALL_WATCHDOG=%s)", v)
		return nil
	}
	probes := []stallwatch.Probe{stallwatch.HTTPProbe(router), stallwatch.LoggerProbe()}
	if db != nil {
		probes = append(probes, stallwatch.DBProbe("db", func(ctx context.Context) error {
			var one int
			return db.GetContext(ctx, &one, "SELECT 1")
		}))
	}
	if bs != nil {
		probes = append(probes, stallwatch.DBProbe("board_db", bs.Ping))
	}
	return stallwatch.Start(ctx, stallwatch.Config{Dir: dataDir, Probes: probes})
}
