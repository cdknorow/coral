//go:build windows

// Package trayevent is how other programs of this user ask a running Coral tray for something on Windows, which has no SIGUSR1: the tray
// listens on a named event and another program sets it (#184 for "show your window", #335 for "quit, the installer is about to replace you").
// An event is this user's alone: its name carries the user's SID and the tray's pid (Local\: this logon session), its owner is that SID and its
// access list grants that SID only. A name someone else made first (another account in the same session, as with runas) is refused by both
// sides: the tray doesn't listen on it and the other program doesn't set it.
package trayevent

import (
	"errors"
	"log"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The kinds of request, each its own event per tray.
const (
	ShowWindow = "CoralTrayShowWindow"
	Quit       = "CoralTrayQuit"
)

// ErrNotOurs: the event at the name is owned by another account.
var ErrNotOurs = errors.New("the tray's event is owned by another account")

// CurrentUserSID is this process's user (never the token's default owner, which is Administrators for an elevated process).
func CurrentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// Name is the event of kind a tray (pid) of user listens on.
func Name(kind string, user *windows.SID, pid int) string {
	return `Local\` + kind + "-" + user.String() + "-" + strconv.Itoa(pid)
}

// OwnedBy reports whether the kernel object h is owned by want.
func OwnedBy(h windows.Handle, want *windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(want) {
		return ErrNotOurs
	}
	return nil
}

// Create creates the tray's event at name: auto-reset, owned by user, its access list user's only (protected, nothing inherited). An event
// already at that name is used only when user owns it.
func Create(name string, user *windows.SID) (windows.Handle, error) {
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
		if oerr := OwnedBy(h, user); oerr != nil {
			windows.CloseHandle(h)
			return 0, oerr
		}
	}
	return h, nil
}

// Signal sets the event at name, once it is known to be user's; no event (no tray listening) or another owner's is an error.
func Signal(name string, user *windows.SID) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE|windows.READ_CONTROL, false, n)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := OwnedBy(h, user); err != nil {
		return err
	}
	return windows.SetEvent(h)
}

// OnSet calls fn each time the event h is set (an event, not a socket: the listener census counts any call named Listen). It returns only when waiting on the event fails.
func OnSet(h windows.Handle, what string, fn func()) {
	for {
		ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			log.Printf("[TRAY] the %s event stopped (%v)", what, err)
			return
		}
		fn()
	}
}
