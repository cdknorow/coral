//go:build windows

package endsession

import (
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// Restart Manager asks with WM_QUERYENDSESSION (answered yes: the process can close) and then WM_ENDSESSION; the stop runs once, and says
// whether an installer (ENDSESSION_CLOSEAPP) or the end of the session asked. A cancelled end (wParam 0) and other messages run nothing.
func TestTheEndOfSessionMessagesRunTheStopOnce(t *testing.T) {
	var once sync.Once
	var calls []bool
	end := func(closeApp bool) { calls = append(calls, closeApp) }

	handled, r := decide(wmQueryEndSession, 0, EndSessionCloseApp, true, &once, end)
	assert.True(t, handled)
	assert.Equal(t, uintptr(1), r, "yes, this process can close")
	assert.Empty(t, calls, "asking is not closing")

	handled, _ = decide(wmEndSession, 0, EndSessionCloseApp, true, &once, end)
	assert.True(t, handled)
	assert.Empty(t, calls, "a cancelled end stops nothing")

	decide(wmEndSession, 1, EndSessionCloseApp, true, &once, end)
	decide(wmEndSession, 1, 0, true, &once, end)
	assert.Equal(t, []bool{true}, calls, "the stop runs once, and knows an installer asked")

	var once2 sync.Once
	calls = nil
	decide(wmEndSession, 1, 0, true, &once2, end)
	assert.Equal(t, []bool{false}, calls, "the end of the session")

	handled, _ = decide(0x0200, 0, 0, true, &once2, end) // WM_MOUSEMOVE
	assert.False(t, handled)

	var once3 sync.Once
	calls = nil
	handled, _ = decide(wmEndSession, 1, EndSessionCloseApp, false, &once3, end)
	assert.True(t, handled)
	assert.Empty(t, calls, "a posted WM_ENDSESSION (any program can post one) stops nothing")
}

// The window exists, is a top-level one Windows can find by its class (what Restart Manager enumerates), is never shown, and answers the
// messages Restart Manager sends to it by running the stop, as an installer closing Coral would.
func TestWatchMakesAHiddenTopLevelWindowThatRunsTheStop(t *testing.T) {
	ended := make(chan bool, 2)
	require.NoError(t, Watch("Coral test", func(closeApp bool) { ended <- closeApp }))
	cls, _ := windows.UTF16PtrFromString("CoralEndSessionWindow")
	h, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(cls)), 0)
	require.NotZero(t, h, "a top-level window of the class")
	vis, _, _ := user32.NewProc("IsWindowVisible").Call(h)
	assert.Zero(t, vis, "never shown")

	// Posted, as any program on the desktop can (QA on #335): nothing runs.
	installerAsked.Store(false) // decide's test above sets it
	post := user32.NewProc("PostMessageW")
	post.Call(h, wmEndSession, 1, EndSessionCloseApp)
	select {
	case <-ended:
		t.Fatal("a posted WM_ENDSESSION ran the stop")
	case <-time.After(300 * time.Millisecond):
	}
	assert.False(t, InstallerAsked())

	send := user32.NewProc("SendMessageW")
	r, _, _ := send.Call(h, wmQueryEndSession, 0, EndSessionCloseApp)
	assert.Equal(t, uintptr(1), r, "it may close")
	send.Call(h, wmEndSession, 1, EndSessionCloseApp)
	select {
	case closeApp := <-ended:
		assert.True(t, closeApp, "Restart Manager asked")
		assert.True(t, InstallerAsked())
	case <-time.After(5 * time.Second):
		t.Fatal("the stop did not run")
	}
	send.Call(h, wmEndSession, 1, EndSessionCloseApp)
	select {
	case <-ended:
		t.Fatal("the stop ran twice")
	case <-time.After(200 * time.Millisecond):
	}
	assert.Error(t, Watch("again", func(bool) {}), "once per process")
}

// Guard keeps a posted or cancelled WM_ENDSESSION from a window another library owns (the tray icon's), and passes a sent one on.
func TestGuardPassesOnlyASentEndOfSession(t *testing.T) {
	got := make(chan uintptr, 4)
	ready := make(chan uintptr, 1)
	go func() {
		runtime.LockOSThread()
		cls, _ := windows.UTF16PtrFromString("CoralGuardTestWindow")
		proc := syscall.NewCallback(func(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
			if message == wmEndSession {
				got <- wParam
				return 0
			}
			r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
			return r
		})
		wc := wndClassEx{wndProc: proc, className: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		h, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		ready <- h
		var m msg
		for {
			if r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	h := <-ready
	require.NotZero(t, h)
	require.NoError(t, Guard("CoralGuardTestWindow"))
	installerAsked.Store(false)

	user32.NewProc("PostMessageW").Call(h, wmEndSession, 1, EndSessionCloseApp)
	user32.NewProc("SendMessageW").Call(h, wmEndSession, 0, EndSessionCloseApp)
	select {
	case w := <-got:
		t.Fatalf("a posted or cancelled WM_ENDSESSION reached the window (wParam %d)", w)
	case <-time.After(300 * time.Millisecond):
	}
	user32.NewProc("SendMessageW").Call(h, wmEndSession, 1, EndSessionCloseApp)
	select {
	case w := <-got:
		assert.Equal(t, uintptr(1), w)
	case <-time.After(5 * time.Second):
		t.Fatal("a sent WM_ENDSESSION did not reach the window")
	}
	assert.True(t, InstallerAsked())
}
