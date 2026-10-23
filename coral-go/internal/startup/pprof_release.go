//go:build !dev

package startup

import (
	"context"
	"log"
	"os"
)

// startPprof in a shipped Coral (#6204): there is no profiler in this build, so CORAL_PPROF_ADDR turns nothing on. It is not silent: someone who set it is told it was ignored, once, at startup.
// A dev build (go build -tags dev, make dev) has the profiler (pprof.go).
func startPprof(context.Context) {
	if os.Getenv("CORAL_PPROF_ADDR") == "" {
		return
	}
	log.Print("[pprof] CORAL_PPROF_ADDR is ignored: this build has no profiler (only a dev build does)")
}
