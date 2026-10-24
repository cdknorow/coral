//go:build windows

package endsession

import (
	"sync"
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

	handled, r := decide(wmQueryEndSession, 0, EndSessionCloseApp, &once, end)
	assert.True(t, handled)
	assert.Equal(t, uintptr(1), r, "yes, this process can close")
	assert.Empty(t, calls, "asking is not closing")

	handled, _ = decide(wmEndSession, 0, EndSessionCloseApp, &once, end)
	assert.True(t, handled)
	assert.Empty(t, calls, "a cancelled end stops nothing")

	decide(wmEndSession, 1, EndSessionCloseApp, &once, end)
	decide(wmEndSession, 1, 0, &once, end)
	assert.Equal(t, []bool{true}, calls, "the stop runs once, and knows an installer asked")

	var once2 sync.Once
	calls = nil
	decide(wmEndSession, 1, 0, &once2, end)
	assert.Equal(t, []bool{false}, calls, "the end of the session")

	handled, _ = decide(0x0200, 0, 0, &once2, end) // WM_MOUSEMOVE
	assert.False(t, handled)
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

	send := user32.NewProc("SendMessageW")
	r, _, _ := send.Call(h, wmQueryEndSession, 0, EndSessionCloseApp)
	assert.Equal(t, uintptr(1), r, "it may close")
	send.Call(h, wmEndSession, 1, EndSessionCloseApp)
	select {
	case closeApp := <-ended:
		assert.True(t, closeApp, "Restart Manager asked")
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
