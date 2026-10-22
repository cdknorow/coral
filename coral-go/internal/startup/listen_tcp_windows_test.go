//go:build windows

package startup

import (
	"context"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// reuseAddrLC listens with SO_REUSEADDR, which on Windows lets a socket bind an address and port another socket already holds,
// unless that one holds it exclusively.
var reuseAddrLC = net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}}

// wildcardBinds: the cases that listen on 0.0.0.0 and [::] run only on request, since a first listen on every interface makes
// Windows Firewall ask about the test binary (a new path at every build) on the person's desktop.
func wildcardBinds() bool { return os.Getenv("CORAL_TEST_WILDCARD_BIND") == "1" }

// #224 (reproduced live by the Security Reviewer): another socket on Coral's port takes this computer's connections to it,
// either by binding the same address with SO_REUSEADDR or, with Coral on 0.0.0.0, by binding the more specific 127.0.0.1 (the
// more specific address wins). Coral's listeners hold their port exclusively: both are refused while Coral runs.
func TestNobodyBindsCoralsPortWhileItRuns(t *testing.T) {
	type bind struct{ coral, other string }
	cases := []bind{{"127.0.0.1", "127.0.0.1"}, {"[::1]", "[::1]"}}
	if wildcardBinds() {
		cases = append(cases, bind{"0.0.0.0", "127.0.0.1"}, bind{"[::]", "[::1]"})
	}
	for _, c := range cases {
		coral, err := listenTCP(c.coral + ":0")
		if err != nil {
			t.Logf("no %s here: %v", c.coral, err)
			continue
		}
		port := strconv.Itoa(coral.Addr().(*net.TCPAddr).Port)
		if ln, err := reuseAddrLC.Listen(context.Background(), "tcp", c.other+":"+port); !assert.Error(t, err, "Coral on %s:%s: an SO_REUSEADDR bind of %s:%s is refused", c.coral, port, c.other, port) {
			ln.Close()
		}
		if c.coral != c.other {
			if ln, err := net.Listen("tcp", c.other+":"+port); !assert.Error(t, err, "Coral on %s:%s: a plain bind of the more specific %s:%s is refused", c.coral, port, c.other, port) {
				ln.Close()
			}
		}
		coral.Close()
	}
}

// Coral's socket holds SO_EXCLUSIVEADDRUSE, read back from the socket itself: the check that runs everywhere, on loopback only.
func TestCoralsListenerHoldsItsPortExclusively(t *testing.T) {
	ln, err := listenTCP("127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	raw, err := ln.(*net.TCPListener).SyscallConn()
	require.NoError(t, err)
	var val int
	var gerr error
	require.NoError(t, raw.Control(func(fd uintptr) {
		val, gerr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse)
	}))
	require.NoError(t, gerr)
	assert.Equal(t, 1, val, "Coral's listener holds its port exclusively")
}

// The takeover itself, both ways (on request: CORAL_TEST_WILDCARD_BIND=1): a plain 0.0.0.0 listener lets a second socket bind
// the more specific 127.0.0.1 of its port, the one Coral now refuses.
func TestWithoutExclusiveTheMoreSpecificBindGetsIn(t *testing.T) {
	if !wildcardBinds() {
		t.Skip("listens on 0.0.0.0 (a Windows Firewall prompt for the test binary): set CORAL_TEST_WILDCARD_BIND=1")
	}
	plain, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer plain.Close()
	port := strconv.Itoa(plain.Addr().(*net.TCPAddr).Port)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err == nil {
		ln.Close()
	}
	assert.NoError(t, err, "a plain wildcard listener leaves 127.0.0.1 of its port open to another socket (what #224 closes)")
}

// A restart is not held up: re-listening on the port right after Coral closes it works, with the old connection in TIME_WAIT.
func TestCoralCanListenAgainRightAfterClosing(t *testing.T) {
	first, err := listenTCP("127.0.0.1:0")
	require.NoError(t, err)
	port := strconv.Itoa(first.Addr().(*net.TCPAddr).Port)
	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	require.NoError(t, err)
	if conn, err := first.Accept(); err == nil {
		conn.Close() // closed by the server first: its side goes to TIME_WAIT
	}
	c.Close()
	first.Close()
	again, err := listenTCP("127.0.0.1:" + port)
	require.NoError(t, err, "Coral listens on its port again right after closing it")
	again.Close()
}
