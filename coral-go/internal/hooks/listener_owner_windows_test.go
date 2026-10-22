//go:build windows

package hooks

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	prevPIDs, prevOwner, prevFolders := listenerPIDs, pidOwner, coralFolders
	listenerPIDs, pidOwner = pids, owner
	listenerVerified = sync.Map{}
	t.Cleanup(func() {
		listenerPIDs, pidOwner, coralFolders = prevPIDs, prevOwner, prevFolders
		listenerVerified = sync.Map{}
	})
}

// #224 B, the real system lookups: a listener this process opens on 127.0.0.1 and on [::1] is found as the one a connection would
// reach, owned by this user. A listener in this very process (a test's in-process server) passes: a tool never listens on
// Coral's port itself. The image rule for any other process is TestTheImageMustBeCoralFromItsOwnFolder's. Nothing listening is fine.
func TestTheRealLookupsFindTheListenerAndEnforceTheImage(t *testing.T) {
	resetListenerCheck(t, tcpListenerPIDs, processOwner)
	me, err := currentUserSID()
	require.NoError(t, err)
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
		assert.Equal(t, []uint32{uint32(os.Getpid())}, pids, "this process is the listener a connection to %s reaches", ln.Addr())
		sid, image, err := processOwner(uint32(os.Getpid()))
		require.NoError(t, err)
		assert.True(t, sid.Equals(me), "owned by this user")
		assert.NoError(t, checkListener(host, port), "this very process (%s) listening: passes", image)
		ln.Close()
	}
	assert.NoError(t, checkListener("127.0.0.1", 1), "nothing listening: the connection fails by itself")
}

// The listener that would WIN is the one checked (the Security Reviewer's #224 finding): a listener on the address itself
// takes the connection over one on its family's wildcard, so with both present only the specific one counts.
func TestTheWinningListenerIsTheMostSpecific(t *testing.T) {
	v4, v6 := net.IPv4(127, 0, 0, 1).To4(), net.IPv6loopback
	rows := []listenRow{
		{net.IPv4zero.To4(), 8450, 100}, // Coral on 0.0.0.0
		{v4, 8450, 200},                 // someone on 127.0.0.1, same port: wins for 127.0.0.1
		{net.IPv6unspecified, 8450, 300},
		{v4, 9999, 400}, // another port
	}
	assert.Equal(t, []uint32{200}, winningPIDs(rows, v4, 8450), "127.0.0.1 over 0.0.0.0")
	assert.Equal(t, []uint32{300}, winningPIDs(rows, v6, 8450), "only [::]: it takes ::1")
	assert.Equal(t, []uint32{100}, winningPIDs(rows[:1], v4, 8450), "only 0.0.0.0: it takes 127.0.0.1")
	assert.Empty(t, winningPIDs(rows, v4, 1234), "nothing on that port")
	assert.Equal(t, []uint32{500}, winningPIDs(append(rows, listenRow{v6, 8450, 500}), v6, 8450), "::1 over ::")
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

// The image is enforced: this user's listener must be coral.exe or coral-tray.exe in a folder Coral runs from (the running
// tool's own folder, covering a dev build, or the Coral install); a renamed binary or another folder gets no token. Each
// address is checked once per process, and a request with no token isn't checked.
func TestTheImageMustBeCoralFromItsOwnFolder(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	folder := t.TempDir()
	for _, c := range []struct {
		image string
		ok    bool
	}{
		{folder + `\coral-tray.exe`, true},
		{folder + `\coral.exe`, true},
		{folder + `\CORAL-TRAY.EXE`, true},
		{folder + `\coral-dev-build.exe`, false},
		{`C:\elsewhere\coral.exe`, false},
		{"", false},
	} {
		image := c.image
		calls := 0
		resetListenerCheck(t, func(string, int) ([]uint32, error) { calls++; return []uint32{77}, nil },
			func(uint32) (*windows.SID, string, error) { return me, image, nil })
		coralFolders = func() []string { return []string{folder} }
		t.Setenv("CORAL_URL", "http://127.0.0.1:8450")
		t.Setenv("CORAL_HOST", "")
		rec := &recorder{}
		client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", token: "tok", hosts: configuredHosts()}}
		for i := 0; i < 2; i++ {
			req, _ := http.NewRequest("GET", "http://127.0.0.1:8450/api/board", nil)
			_, err := client.Do(req)
			if c.ok {
				require.NoError(t, err, image)
			} else {
				var foreign *ForeignListenerError
				require.True(t, errors.As(err, &foreign), "%s: refused: %v", image, err)
			}
		}
		if c.ok {
			require.Len(t, rec.got, 2)
			assert.Equal(t, "tok", rec.got[0].Header.Get(SessionTokenHeader), image)
		} else {
			assert.Empty(t, rec.got, "%s: nothing sent", image)
		}
		assert.Equal(t, 1, calls, "%s: checked once per address", image)
	}
	assert.True(t, isCoralImage(`C:\Program Files\Coral\coral-tray.exe`, allowedCoralFolders()) || os.Getenv("ProgramFiles") == "", "the installed folder is allowed")

	calls := 0
	resetListenerCheck(t, func(string, int) ([]uint32, error) { calls++; return []uint32{77}, nil },
		func(uint32) (*windows.SID, string, error) { return me, `C:\x\other.exe`, nil })
	rec := &recorder{}
	noToken := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", hosts: configuredHosts()}}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:9999/api/board", nil)
	_, err = noToken.Do(req)
	require.NoError(t, err)
	assert.Zero(t, calls, "no token: nothing to protect, no check")
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

// A test runs its CLI as a child of the test binary, which serves on the port: the listener is then this tool's own
// executable file, which passes; a different program in another folder still does not.
func TestAListenerRunningThisToolsOwnExecutablePasses(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)
	resetListenerCheck(t, func(string, int) ([]uint32, error) { return []uint32{77}, nil },
		func(uint32) (*windows.SID, string, error) { return me, exe, nil })
	coralFolders = func() []string { return nil }
	assert.NoError(t, checkListener("127.0.0.1", 8450), "the listener is %s, this tool's own executable", exe)
	listenerVerified = sync.Map{}
	pidOwner = func(uint32) (*windows.SID, string, error) { return me, `C:\elsewhere\` + filepath.Base(exe), nil }
	var foreign *ForeignListenerError
	assert.True(t, errors.As(checkListener("127.0.0.1", 8450), &foreign), "the same name in another folder is not this tool's executable")
}

// Security's spec for #224 B: the listener that WINS the connection is the one checked, in both orders. Coral (this user) on
// 0.0.0.0 with another account's program on 127.0.0.1 of the same port: the impostor wins, refused. Another account's program
// on 0.0.0.0 with Coral on 127.0.0.1: Coral wins, allowed. The same for [::] and [::1].
func TestTheWinnerIsCheckedInBothOrders(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	folder := t.TempDir()
	owners := map[uint32]*windows.SID{100: me, 200: system}
	for _, c := range []struct {
		name        string
		rows        []listenRow
		host        string
		wantRefused bool
	}{
		{"0.0.0.0 mine, 127.0.0.1 foreign", []listenRow{{net.IPv4zero.To4(), 8450, 100}, {net.IPv4(127, 0, 0, 1).To4(), 8450, 200}}, "127.0.0.1", true},
		{"0.0.0.0 foreign, 127.0.0.1 mine", []listenRow{{net.IPv4zero.To4(), 8450, 200}, {net.IPv4(127, 0, 0, 1).To4(), 8450, 100}}, "127.0.0.1", false},
		{"[::] mine, [::1] foreign", []listenRow{{net.IPv6unspecified, 8450, 100}, {net.IPv6loopback, 8450, 200}}, "::1", true},
		{"[::] foreign, [::1] mine", []listenRow{{net.IPv6unspecified, 8450, 200}, {net.IPv6loopback, 8450, 100}}, "::1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows := c.rows
			resetListenerCheck(t, tcpListenerPIDs, func(pid uint32) (*windows.SID, string, error) { return owners[pid], folder + `\coral-tray.exe`, nil })
			tcpRows = func(uint32) ([]listenRow, error) { return rows, nil }
			coralFolders = func() []string { return []string{folder} }
			err := checkListener(c.host, 8450)
			if c.wantRefused {
				var foreign *ForeignListenerError
				require.True(t, errors.As(err, &foreign), "refused: %v", err)
				assert.Equal(t, uint32(200), foreign.PID, "the impostor on the specific address is the one named")
			} else {
				assert.NoError(t, err, "Coral holds the specific address, so it gets the connection")
			}
		})
	}
}

// Security's note on #224: a tool running as another local account (Codex's elevated Windows sandbox) still reaches the real
// user's Coral: the listener's owner is compared with the owner of Coral's data folder and of the session's token file too, not
// only with the tool's own account. A service account owning the data folder is never added.
func TestTheDataFolderOwnerIsAnAllowedListenerOwner(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	sandbox, err := windows.StringToSid("S-1-5-21-1111111111-2222222222-3333333333-1501") // the sandbox's own account
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	folder := t.TempDir()

	resetListenerCheck(t, func(string, int) ([]uint32, error) { return []uint32{100}, nil },
		func(uint32) (*windows.SID, string, error) { return me, folder + `\coral.exe`, nil })
	coralFolders = func() []string { return []string{folder} }
	selfSID = func() (*windows.SID, error) { return sandbox, nil }
	fileOwner = func(string) (*windows.SID, error) { return me, nil }
	assert.NoError(t, checkListener("127.0.0.1", 8450), "a sandbox account reaches the Coral of the user who owns the data folder")

	listenerVerified = sync.Map{}
	fileOwner = func(string) (*windows.SID, error) { return nil, windows.ERROR_FILE_NOT_FOUND }
	var foreign *ForeignListenerError
	assert.True(t, errors.As(checkListener("127.0.0.1", 8450), &foreign), "without the data folder's owner, the real user's Coral is not the sandbox account's")

	listenerVerified = sync.Map{}
	pidOwner = func(uint32) (*windows.SID, string, error) { return system, folder + `\coral.exe`, nil }
	fileOwner = func(string) (*windows.SID, error) { return system, nil }
	assert.True(t, errors.As(checkListener("127.0.0.1", 8450), &foreign), "a data folder owned by SYSTEM does not make a SYSTEM listener Coral")
}
