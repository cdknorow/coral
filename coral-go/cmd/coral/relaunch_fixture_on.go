//go:build relaunchfixture

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/server/routes"
)

// applyRelaunchFixtureBuild is the relaunch test seam's startup check (#5452, Security's conditions 4 and 5). A build made with the relaunchfixture tag:
//   - refuses --remote and any --host that is not a loopback address, and listens on 127.0.0.1 when no host is given, so it can never be exposed by mistake;
//   - connects to no Coral Hub unless the test names one with --cloud-url itself (CORAL_CLOUD_URL and the build's default are ignored);
//   - refuses to start unless CORAL_RELAUNCH_FIXTURE_DIR names an owner-only folder holding a valid fixture (routes.CheckRelaunchFixture).
//
// Every other build compiles the no-op in relaunch_fixture_off.go.
func applyRelaunchFixtureBuild(cfg *config.Config, hostFlag string, remote *bool, cloudURLGiven bool) error {
	if *remote {
		return errors.New("a relaunchfixture build refuses --remote: it listens on this computer only")
	}
	if hostFlag != "" {
		for _, h := range strings.Split(hostFlag, ",") {
			h = strings.TrimSpace(h)
			if ip := net.ParseIP(h); !(h == "localhost" || (ip != nil && ip.IsLoopback())) {
				return fmt.Errorf("a relaunchfixture build refuses --host %q: it listens on this computer only (127.0.0.1, ::1 or localhost)", h)
			}
		}
	} else {
		cfg.Host, cfg.HostExplicit = "127.0.0.1", true
	}
	if !cloudURLGiven {
		cfg.CloudURL, cfg.CloudURLExplicit = config.CloudURLSetting("", true), false // off: the test connects to a hub only by naming it
	}
	// It never runs on the data folder a real Coral uses (~/.coral): a test server has its own CORAL_DATA_DIR or --home, so a fixture build cannot open, migrate or
	// rewrite anyone's live state (#5452 review).
	if live := liveDataDir(); live != "" && sameDir(cfg.CoralDir(), live) {
		return fmt.Errorf("a relaunchfixture build refuses the default data folder %s: give it its own CORAL_DATA_DIR or --home", live)
	}
	if err := routes.CheckRelaunchFixture(); err != nil {
		return fmt.Errorf("a relaunchfixture build needs a valid fixture folder: %w", err)
	}
	log.Printf("[STARTUP] relaunchfixture build: relaunch routes ON with a fixture updater from CORAL_RELAUNCH_FIXTURE_DIR; loopback only; no hub unless --cloud-url is given")
	return nil
}

// liveDataDir is the default data folder of this user (~/.coral), "" when the home folder is unknown.
func liveDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".coral")
}

// sameDir reports whether a and b name the same folder, as given or with links resolved.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}
