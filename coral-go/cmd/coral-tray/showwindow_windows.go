//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/safego"
)

// The Windows hand-over (#184): Windows has no SIGUSR1, so the running tray listens on a named event and a second start of the app sets it.
// The event is this user's alone: its name carries the user's SID and the tray's pid (Local\: this logon session), its owner is that SID and
// its access list grants that SID only. A name someone else made first (another account in the same session, as with runas) is refused by
// both sides: the tray doesn't listen on it and a second start doesn't set it, and starts as before.

// errNotOurs: the event at the name is owned by another account.
var errNotOurs = errors.New("the hand-over event is owned by another account")

// currentUserSID is this process's user (never the token's default owner, which is Administrators for an elevated process).
func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// showEventName is the event a tray (pid) of this user listens on.
func showEventName(user *windows.SID, pid int) string {
	return `Local\CoralTrayShowWindow-` + user.String() + "-" + strconv.Itoa(pid)
}

// ownedBy reports whether the kernel object h is owned by want.
func ownedBy(h windows.Handle, want *windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(want) {
		return errNotOurs
	}
	return nil
}

// createShowEvent creates the tray's event at name: auto-reset, owned by user, its access list user's only (protected, nothing inherited).
// An event already at that name is used only when user owns it.
func createShowEvent(name string, user *windows.SID) (windows.Handle, error) {
	sd, err := windows.SecurityDescriptorFromString("O:" + user.String() + "D:P(A;;GA;;;" + user.String() + ")")
	if err != nil {
		return 0, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateEvent(sa, 0, 0, n)
	if h == 0 {
		return 0, err
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if oerr := ownedBy(h, user); oerr != nil {
			windows.CloseHandle(h)
			return 0, oerr
		}
	}
	return h, nil
}

// signalShowEvent sets the event at name, once it is known to be user's; no event (no tray listening) or another owner's is an error.
func signalShowEvent(name string, user *windows.SID) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE|windows.READ_CONTROL, false, n)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := ownedBy(h, user); err != nil {
		return err
	}
	return windows.SetEvent(h)
}

// listenShowEvent calls show each time the event h is set. It never returns.
func listenShowEvent(h windows.Handle, show func()) {
	for {
		ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			log.Printf("[TRAY] the hand-over event stopped (%v): a second start of the app now starts as before", err)
			return
		}
		show()
	}
}

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
	user, err := currentUserSID()
	if err != nil {
		log.Printf("[TRAY] no hand-over event (this user: %v): a second start of the app starts as before", err)
		return
	}
	h, err := createShowEvent(showEventName(user, os.Getpid()), user)
	if err != nil {
		log.Printf("[TRAY] no hand-over event (%v): a second start of the app starts as before", err)
		return
	}
	go func() {
		defer safego.Recover("coral-tray/showwindow_windows:listen")
		listenShowEvent(h, show)
	}()
}
