//go:build windows

// Package endsession lets a Coral process with no window of its own be closed by Windows the normal way (#335). Restart Manager, which an
// installer uses to close the programs whose files it replaces, and the end of a session ask a program to close by sending
// WM_QUERYENDSESSION and WM_ENDSESSION to its top-level windows; a program with none (coral-tray's server child, which only serves) can't be
// asked, and the installer reports "unable to automatically close all requested applications" and goes on with the files in use. Watch gives
// the process a hidden top-level window that answers those messages by running its normal stop.
package endsession

import (
	"errors"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/safego"
)

const (
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmClose           = 0x0010
	wmDestroy         = 0x0002
	// EndSessionCloseApp is WM_ENDSESSION's lParam when Restart Manager (not a logoff) asks the program to close.
	EndSessionCloseApp = 0x00000001
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procInSendMessageEx          = user32.NewProc("InSendMessageEx")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
)

// ismexSend is InSendMessageEx's flag for a message another thread or process SENT (SendMessage, as Restart Manager and the end of the
// session do); a POSTED message has it clear, and any program on the desktop can post one (QA on #335: a posted WM_ENDSESSION stopped the
// server in under a second). Sent is not the same as "from Restart Manager": any program in the session, another account's too, can send
// one, and nothing tells the sender apart; that is accepted (Security on #155).
const ismexSend = 0x00000001

// sentByAnother reports whether the message being handled was sent by another thread or process, not posted.
func sentByAnother() bool {
	r, _, _ := procInSendMessageEx.Call(0)
	return r&ismexSend != 0
}

// installerAsked is set when a sent WM_ENDSESSION that really ends says ENDSESSION_CLOSEAPP: Restart Manager closing the program for an
// installer, not a logoff or a shutdown (which never set it).
var installerAsked atomic.Bool

// InstallerAsked reports whether Windows closed this process for an installer (Restart Manager), as opposed to a logoff or a shutdown.
func InstallerAsked() bool { return installerAsked.Load() }

type wndClassEx struct {
	size, style            uint32
	wndProc                uintptr
	clsExtra, wndExtra     int32
	instance, icon, cursor windows.Handle
	background             windows.Handle
	menuName, className    *uint16
	iconSm                 windows.Handle
}

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

var (
	mu      sync.Mutex
	onEnd   func(closeApp bool)
	endOnce sync.Once
)

// decide is what the window procedure does with a message: whether it is one of the end-of-session messages, and the result to return. It is the
// whole decision, apart from the window, so a test can drive it: WM_QUERYENDSESSION is always answered yes (the process can close);
// WM_ENDSESSION that was sent (not posted) and says the session (or the program) really ends runs onEnd once, and nothing else; one that
// says it was cancelled, or was posted, does nothing.
func decide(message uint32, wParam, lParam uintptr, sent bool, once *sync.Once, end func(closeApp bool)) (handled bool, result uintptr) {
	switch message {
	case wmQueryEndSession:
		return true, 1
	case wmEndSession:
		if wParam != 0 && sent {
			closeApp := lParam&EndSessionCloseApp != 0
			if closeApp {
				installerAsked.Store(true)
			}
			once.Do(func() { end(closeApp) })
		}
		return true, 0
	}
	return false, 0
}

func wndProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	mu.Lock()
	end := onEnd
	mu.Unlock()
	if handled, r := decide(message, wParam, lParam, sentByAnother(), &endOnce, end); handled {
		return r
	}
	switch message {
	case wmClose:
		// Never destroyed on request: any program can post WM_CLOSE, and without the window a later Restart Manager close couldn't
		// reach this process (QA on #155). It goes when the process ends.
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

// Watch gives this process a hidden top-level window titled title, on a thread of its own, and calls end (once) when Windows asks the
// process to close: closeApp is true when Restart Manager asked (an installer), false at the end of the session. end must stop the process
// the normal way and may take a few seconds; Windows waits for the process to end after WM_ENDSESSION returns. Call it once per process.
func Watch(title string, end func(closeApp bool)) error {
	mu.Lock()
	if onEnd != nil {
		mu.Unlock()
		return errors.New("endsession: already watching")
	}
	onEnd = end
	mu.Unlock()
	ready := make(chan error, 1)
	go func() {
		defer safego.Recover("endsession:window")
		runtime.LockOSThread() // the window's messages come to the thread that made it
		cls, _ := windows.UTF16PtrFromString("CoralEndSessionWindow")
		name, _ := windows.UTF16PtrFromString(title)
		wc := wndClassEx{wndProc: syscall.NewCallback(wndProc), className: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			ready <- err
			return
		}
		// A top-level window (no parent, never HWND_MESSAGE: message-only windows get no end-of-session messages), never shown.
		h, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
		if h == 0 {
			ready <- err
			return
		}
		ready <- nil
		var m msg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				log.Printf("[ENDSESSION] the window's message loop ended")
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	return <-ready
}

// Guard filters WM_ENDSESSION for a window of this process that another library owns (the tray icon's, class className): one that was
// posted, or says the end was cancelled, never reaches it; a real one is recorded (InstallerAsked) and passed on. The systray library ends
// the tray on any WM_ENDSESSION, posted or not.
func Guard(className string) error {
	h := findOwnWindow(className)
	if h == 0 {
		return errors.New("endsession: no " + className + " window in this process")
	}
	guardMu.Lock()
	defer guardMu.Unlock()
	if guardedOld != 0 {
		return errors.New("endsession: already guarding a window")
	}
	const gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC, -4
	old, _, err := procSetWindowLongPtrW.Call(h, gwlpWndProc, syscall.NewCallback(guardProc))
	if old == 0 {
		return err
	}
	guardedOld = old
	return nil
}

var (
	guardMu    sync.Mutex
	guardedOld uintptr
)

func guardProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	if message == wmEndSession {
		if wParam == 0 || !sentByAnother() {
			return 0
		}
		if lParam&EndSessionCloseApp != 0 {
			installerAsked.Store(true)
		}
	}
	guardMu.Lock()
	old := guardedOld
	guardMu.Unlock()
	r, _, _ := procCallWindowProcW.Call(old, uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

// findOwnWindow is this process's top-level window of the class, or 0.
func findOwnWindow(className string) uintptr {
	me := windows.GetCurrentProcessId()
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != me {
			return 1
		}
		buf := make([]uint16, 256)
		n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if windows.UTF16ToString(buf[:n]) == className {
			found = hwnd
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}
