//go:build windows

package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/trayevent"
)

// The Windows hand-over (#184): Windows has no SIGUSR1, so the running tray listens on a named event and a second start of the app sets it.
// The event is this user's alone (internal/trayevent): its name carries the user's SID and the tray's pid, its owner is that SID and its
// access list grants that SID only. A name someone else made first is refused by both sides, and a second start then starts as before.

// errNotOurs: the event at the name is owned by another account.
var errNotOurs = trayevent.ErrNotOurs

func currentUserSID() (*windows.SID, error) { return trayevent.CurrentUserSID() }

// showEventName is the event a tray (pid) of this user listens on.
func showEventName(user *windows.SID, pid int) string {
	return trayevent.Name(trayevent.ShowWindow, user, pid)
}

func ownedBy(h windows.Handle, want *windows.SID) error { return trayevent.OwnedBy(h, want) }

func createShowEvent(name string, user *windows.SID) (windows.Handle, error) {
	return trayevent.Create(name, user)
}

func signalShowEvent(name string, user *windows.SID) error { return trayevent.Signal(name, user) }

// listenShowEvent calls show each time the event h is set. It never returns.
func listenShowEvent(h windows.Handle, show func()) { trayevent.OnSet(h, "hand-over", show) }

// askTrayToShowWindow is a second start of the app asking the running tray (pid) to show its window: it sets that tray's event. An error
// (no tray listening, an event of another account's) means no hand-over, and the start goes on as before.
func askTrayToShowWindow(pid int) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	return signalShowEvent(showEventName(user, pid), user)
}

// onShowWindowSignal calls show whenever another start of the app asks this tray to show its window.
func onShowWindowSignal(show func()) {
	onTrayEvent(trayevent.ShowWindow, "hand-over", show)
}

// onTrayEvent listens on this tray's event of kind and calls fn each time it is set. With no event (this user unknown, a name another
// account made) it logs and listens to nothing.
func onTrayEvent(kind, what string, fn func()) {
	user, err := currentUserSID()
	if err != nil {
		log.Printf("[TRAY] no %s event (this user: %v)", what, err)
		return
	}
	h, err := trayevent.Create(trayevent.Name(kind, user, os.Getpid()), user)
	if err != nil {
		log.Printf("[TRAY] no %s event (%v)", what, err)
		return
	}
	go func() {
		defer safego.Recover("coral-tray/showwindow_windows:" + what)
		trayevent.OnSet(h, what, fn)
	}()
}
