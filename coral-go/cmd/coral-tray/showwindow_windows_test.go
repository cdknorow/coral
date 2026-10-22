//go:build windows

package main

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// testEventName is an event name no tray uses: this test process's pid, with the test's name.
func testEventName(t *testing.T, user *windows.SID) string {
	return showEventName(user, os.Getpid()) + "-" + t.Name()
}

func mustUser(t *testing.T) *windows.SID {
	t.Helper()
	user, err := currentUserSID()
	require.NoError(t, err)
	return user
}

// #184: a second start sets the running tray's event, and the tray shows its window, once per start.
func TestASecondStartMakesTheRunningTrayShowItsWindow(t *testing.T) {
	user := mustUser(t)
	name := testEventName(t, user)
	h, err := createShowEvent(name, user)
	require.NoError(t, err)
	defer windows.CloseHandle(h)
	shown := make(chan struct{}, 4)
	go listenShowEvent(h, func() { shown <- struct{}{} })

	for i := 0; i < 2; i++ {
		require.NoError(t, signalShowEvent(name, user))
		select {
		case <-shown:
		case <-time.After(5 * time.Second):
			t.Fatalf("start %d: the tray was not asked to show its window", i+1)
		}
	}
}

// No tray listening (it quit, or never made its event): no hand-over, and the start goes on as before.
func TestNoTrayListeningIsNoHandOver(t *testing.T) {
	user := mustUser(t)
	assert.Error(t, signalShowEvent(testEventName(t, user), user))
	assert.False(t, handOver(os.Getpid(), askTrayToShowWindow), "this test process is no tray")
}

// The event is this user's alone: owned by the user's SID, and its access list (protected, nothing inherited) grants that SID only.
func TestTheHandOverEventIsThisUsersAlone(t *testing.T) {
	user := mustUser(t)
	h, err := createShowEvent(testEventName(t, user), user)
	require.NoError(t, err)
	defer windows.CloseHandle(h)
	require.NoError(t, ownedBy(h, user))

	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	ctl, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, ctl&windows.SE_DACL_PROTECTED, "nothing inherited")
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl)
	require.Equal(t, uint16(1), dacl.AceCount, "one entry")
	var ace *windows.ACCESS_ALLOWED_ACE
	require.NoError(t, windows.GetAce(dacl, 0, &ace))
	assert.Equal(t, uint8(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType)
	assert.True(t, (*windows.SID)(unsafePointer(&ace.SidStart)).Equals(user), "for this user")
}

// A name someone else made first (another account in this logon session) is refused by both sides: the tray doesn't listen on it, and a
// second start doesn't set it (and starts as before). Here the "someone else" is the expected owner made another account (SYSTEM): the
// event at the name is then not that account's, exactly as a squatted one is not this user's.
func TestAHandOverEventOfAnotherAccountIsRefused(t *testing.T) {
	user := mustUser(t)
	other, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	name := testEventName(t, user)
	h, err := createShowEvent(name, user)
	require.NoError(t, err)
	defer windows.CloseHandle(h)
	shown := make(chan struct{}, 1)
	go listenShowEvent(h, func() { shown <- struct{}{} })

	_, err = createShowEvent(name, other)
	assert.ErrorIs(t, err, errNotOurs, "the tray doesn't listen on another account's event")
	assert.ErrorIs(t, signalShowEvent(name, other), errNotOurs, "a second start doesn't set it")
	select {
	case <-shown:
		t.Fatal("an event of another account was set")
	case <-time.After(300 * time.Millisecond):
	}

	h2, err := createShowEvent(name, user)
	require.NoError(t, err, "the same user's existing event is used")
	windows.CloseHandle(h2)
}

func unsafePointer(p *uint32) unsafe.Pointer { return unsafe.Pointer(p) }
