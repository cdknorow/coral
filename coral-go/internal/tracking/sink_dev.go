//go:build dev

package tracking

import (
	"os"

	"github.com/subgentic/coral/internal/config"
)

// A dev build has no analytics key and so sends nothing. CORAL_TELEMETRY_TEST_SINK=<url> gives it a dummy key and
// points delivery at a local capture endpoint, so a test can run a real server and read its events (#4935). This
// file is compiled into dev builds only: a release binary cannot be redirected.
func init() {
	if url := os.Getenv("CORAL_TELEMETRY_TEST_SINK"); url != "" {
		config.PostHogKey = "dev-test-sink"
		posthogURL = url
	}
}

// SinkAvailable is true in a dev build: CORAL_TELEMETRY_TEST_SINK can point delivery at a local capture endpoint (tests only).
func SinkAvailable() bool { return true }
