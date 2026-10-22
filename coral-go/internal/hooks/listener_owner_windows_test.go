//go:build windows

package hooks

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The hooks tests send to example loopback ports through a recording transport, never a real server: by default nothing is
// listening there as far as the check knows, whatever this computer has on those ports (this one has a service on 8420).
// The tests below set the lookups themselves.
func TestMain(m *testing.M) {
	listenerPIDs = func(string, int) ([]uint32, error) { return nil, nil }
	os.Exit(m.Run())
}

// resetListenerCheck clears the per-process cache and restores the real lookups (or the test's) for one test.
func resetListenerCheck(t *testing.T, pids func(string, int) ([]uint32, error), owner func(uint32) (*windows.SID, string, error)) {
	t.Helper()
	prevPIDs, prevOwner := listenerPIDs, pidOwner
	listenerPIDs, pidOwner = pids, owner
	listenerVerified = sync.Map{}
	t.Cleanup(func() { listenerPIDs, pidOwner = prevPIDs, prevOwner; listenerVerified = sync.Map{} })
}

// #224 B, for real: a listener this process opened (this user's) on 127.0.0.1 and on [::1] passes the check; the system's own
// table and token lookups are used.
func TestThisUsersOwnListenerIsCoral(t *testing.T) {
	resetListenerCheck(t, tcpListenerPIDs, processOwner)
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Logf("no %s here: %v", addr, err)
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		host := ln.Addr().(*net.TCPAddr).IP.String()
		pids, err := tcpListenerPIDs(host, port)
		require.NoError(t, err)
		assert.Contains(t, pids, uint32(os.Getpid()), "the table names this process as the listener on %s", ln.Addr())
		assert.NoError(t, checkListener(host, port), "this user's own listener on %s", ln.Addr())
		ln.Close()
	}
	assert.NoError(t, checkListener("127.0.0.1", 1), "nothing listening: the connection fails by itself")
}

// A listener of another account (on a shared computer, while Coral is down), or one whose owner can't be read, is refused
// before the token goes; a request to it fails with the reason and nothing is sent.
func TestAnotherAccountsListenerGetsNoToken(t *testing.T) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	for name, owner := range map[string]func(uint32) (*windows.SID, string, error){
		"another account": func(uint32) (*windows.SID, string, error) { return system, `C:\evil\listener.exe`, nil },
		"unreadable":      func(uint32) (*windows.SID, string, error) { return nil, "", windows.ERROR_ACCESS_DENIED },
	} {
		t.Run(name, func(t *testing.T) {
			resetListenerCheck(t, func(string, int) ([]uint32, error) { return []uint32{4242}, nil }, owner)
			t.Setenv("CORAL_URL", "http://127.0.0.1:8450")
			t.Setenv("CORAL_HOST", "")
			rec := &recorder{}
			client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", token: "tok", hosts: configuredHosts()}}
			for _, target := range []string{"http://127.0.0.1:8450/api/board", "http://[::1]:8450/api/board", "http://localhost:8450/api/board"} {
				req, _ := http.NewRequest("GET", target, nil)
				_, err := client.Do(req)
				var foreign *ForeignListenerError
				require.Error(t, err, target)
				assert.True(t, errors.As(err, &foreign), "%s: %v", target, err)
				assert.Equal(t, 8450, foreign.Port)
				assert.Equal(t, uint32(4242), foreign.PID)
			}
			assert.Empty(t, rec.got, "no request went out")
		})
	}
}

// The image is a soft check: this user's listener that is not coral.exe or coral-tray.exe (a dev build, a renamed binary) still
// gets the token. A request with no token is not checked at all.
func TestTheImageIsOnlyLoggedAndNoTokenMeansNoCheck(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	calls := 0
	resetListenerCheck(t, func(string, int) ([]uint32, error) { calls++; return []uint32{77}, nil },
		func(uint32) (*windows.SID, string, error) { return me, `D:\dev\coral-dev-build.exe`, nil })
	t.Setenv("CORAL_URL", "http://127.0.0.1:8450")
	t.Setenv("CORAL_HOST", "")
	rec := &recorder{}
	client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", token: "tok", hosts: configuredHosts()}}
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:8450/api/board", nil)
		_, err := client.Do(req)
		require.NoError(t, err)
	}
	require.Len(t, rec.got, 3)
	assert.Equal(t, "tok", rec.got[0].Header.Get(SessionTokenHeader), "a dev build of this user still gets the token")
	assert.Equal(t, 1, calls, "checked once per address for the process, not per request")

	noToken := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", hosts: configuredHosts()}}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:9999/api/board", nil)
	_, err = noToken.Do(req)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "no token: nothing to protect, no check")
}

// The real system's table: the Windows service on this computer's 0.0.0.0:8420 (svchost, another account) is refused, as an
// agent pointed at that port would be; skipped where nothing of another account listens on 8420.
func TestARealForeignListenerOnThisComputer(t *testing.T) {
	resetListenerCheck(t, tcpListenerPIDs, processOwner)
	pids, err := tcpListenerPIDs("127.0.0.1", 8420)
	require.NoError(t, err)
	me, _ := currentUserSID()
	for _, pid := range pids {
		if sid, _, err := processOwner(pid); err != nil || !sid.Equals(me) {
			err := checkListener("127.0.0.1", 8420)
			var foreign *ForeignListenerError
			assert.True(t, errors.As(err, &foreign), "the listener of another account on 8420 (PID %d) is refused: %v", pid, err)
			return
		}
	}
	t.Skip("nothing of another account listens on 8420 here (" + strconv.Itoa(len(pids)) + " listeners)")
}
