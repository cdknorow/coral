//go:build windows

package main

import (
	"log"
	"time"

	"github.com/subgentic/coral/internal/endsession"
	"github.com/subgentic/coral/internal/installrestart"
	"github.com/subgentic/coral/internal/trayevent"
)

// Closing Coral for an install (#335). An MSI upgrade replaces files that Coral's processes have open, so Coral must close first, the normal
// way (agents marked to wake). Two ways ask it:
//   - the installer's helper (cmd/coral-msi-helper) sets this tray's quit event, the way a second start sets its show event;
//   - Restart Manager, which Windows Installer uses, sends WM_QUERYENDSESSION and WM_ENDSESSION to the process's top-level windows: the tray's
//     systray window gets them (its library runs the tray's onExit), and the server child, which has no window of its own, gets a hidden one
//     (internal/endsession) that runs its normal stop.

// onQuitForInstallSignal calls quit whenever the installer's helper asks this tray to close.
func onQuitForInstallSignal(quit func()) {
	onTrayEvent(trayevent.Quit, "quit-for-install", quit)
}

// onSystemClose is the tray closing because Windows asked (Restart Manager for an installer, or the end of the session), not the person:
// it marks Coral to be started again after the install (installrestart; a mark from a logoff is too old by the next install).
func onSystemClose() {
	if err := installrestart.Mark(time.Now()); err != nil {
		log.Printf("[TRAY] Coral won't be started again after the install: %v", err)
	}
}

// watchEndSession gives this process a hidden top-level window so Windows can ask it to close (internal/endsession).
func watchEndSession(title string, end func(closeApp bool)) {
	if err := endsession.Watch(title, end); err != nil {
		log.Printf("[ENDSESSION] no window for Windows to close this process with: %v", err)
	}
}
