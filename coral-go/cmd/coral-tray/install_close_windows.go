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

// onSystemClose is the tray closing because Windows asked, not the person. Only when Restart Manager closed it for an installer
// (ENDSESSION_CLOSEAPP, on a sent WM_ENDSESSION) does it mark Coral to be started again after the install (installrestart): a logoff or a
// shutdown never marks, so a reboot's mark can't start Coral beside the updater's own start (QA on #335).
func onSystemClose() {
	if !endsession.InstallerAsked() {
		return
	}
	if err := installrestart.Mark(time.Now()); err != nil {
		log.Printf("[TRAY] Coral won't be started again after the install: %v", err)
	}
}

// guardTrayWindow keeps a posted WM_ENDSESSION from the tray icon's window, whose library would end the tray on any (internal/endsession).
func guardTrayWindow() {
	if err := endsession.Guard("SystrayClass"); err != nil {
		log.Printf("[ENDSESSION] the tray window is not guarded: %v", err)
	}
}

// watchEndSession gives this process a hidden top-level window so Windows can ask it to close (internal/endsession).
func watchEndSession(title string, end func(closeApp bool)) {
	if err := endsession.Watch(title, end); err != nil {
		log.Printf("[ENDSESSION] no window for Windows to close this process with: %v", err)
	}
}

// allowTrayWindowClose is called just before the tray's own systray.Quit, which posts WM_CLOSE to the icon's window: Guard drops every other WM_CLOSE (#346).
func allowTrayWindowClose() { endsession.AllowClose() }
