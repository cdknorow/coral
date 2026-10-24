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
	user32               = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
)

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
// WM_ENDSESSION that says the session (or the program) really ends runs onEnd once, and nothing else; one that says it was cancelled does
// nothing.
func decide(message uint32, wParam, lParam uintptr, once *sync.Once, end func(closeApp bool)) (handled bool, result uintptr) {
	switch message {
	case wmQueryEndSession:
		return true, 1
	case wmEndSession:
		if wParam != 0 {
			once.Do(func() { end(lParam&EndSessionCloseApp != 0) })
		}
		return true, 0
	}
	return false, 0
}

func wndProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	mu.Lock()
	end := onEnd
	mu.Unlock()
	if handled, r := decide(message, wParam, lParam, &endOnce, end); handled {
		return r
	}
	switch message {
	case wmClose:
		procDestroyWindow.Call(uintptr(hwnd))
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
