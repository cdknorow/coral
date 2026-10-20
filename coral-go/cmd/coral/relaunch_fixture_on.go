//go:build relaunchfixture

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/server/routes"
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
	if err := routes.CheckRelaunchFixture(); err != nil {
		return fmt.Errorf("a relaunchfixture build needs a valid fixture folder: %w", err)
	}
	log.Printf("[STARTUP] relaunchfixture build: relaunch routes ON with a fixture updater from CORAL_RELAUNCH_FIXTURE_DIR; loopback only; no hub unless --cloud-url is given")
	return nil
}
