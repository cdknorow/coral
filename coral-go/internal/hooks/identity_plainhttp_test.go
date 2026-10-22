package hooks

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #224 (the Security Reviewer's #221 F1): the agent's session token never crosses the network in clear text. It goes to its
// Coral over loopback, over https, or over plain http only to an address of this computer itself (a Coral started with
// --host on one LAN address gives its agents that address). Plain http to a configured address that is not this computer is
// refused with a reason, and nothing is sent.
func TestTokenIsNotSentOverPlainHTTPOffThisComputer(t *testing.T) {
	prev := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.5.10"), Mask: net.CIDRMask(24, 32)}, // this computer's LAN address
		}, nil
	}
	t.Cleanup(func() { interfaceAddrs = prev })

	for _, c := range []struct {
		name, coralURL, target string
		token                  string
		wantSent               bool
		wantToken              string
		wantRefused            bool
	}{
		{"loopback http", "http://127.0.0.1:8450", "http://127.0.0.1:8450/api/board", "tok", true, "tok", false},
		{"this computer's LAN address over http (a Coral on --host 192.168.5.10)", "http://192.168.5.10:8450", "http://192.168.5.10:8450/api/board", "tok", true, "tok", false},
		{"another computer over https", "https://203.0.113.7:8450", "https://203.0.113.7:8450/api/board", "tok", true, "tok", false},
		{"another computer over plain http: refused", "http://203.0.113.7:8450", "http://203.0.113.7:8450/api/board", "tok", false, "", true},
		{"a name over plain http (not looked up): refused", "http://coral.lan:8450", "http://coral.lan:8450/api/board", "tok", false, "", true},
		{"no token: nothing to protect, sent as before", "http://203.0.113.7:8450", "http://203.0.113.7:8450/api/board", "", true, "", false},
		{"an unrelated host: sent without the token, as before", "http://127.0.0.1:8450", "http://198.51.100.9/x", "tok", true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CORAL_URL", c.coralURL)
			t.Setenv("CORAL_HOST", "")
			rec := &recorder{}
			client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", token: c.token, hosts: configuredHosts()}}
			req, _ := http.NewRequest("GET", c.target, nil)
			_, err := client.Do(req)
			var refused *PlainHTTPTokenError
			if c.wantRefused {
				require.Error(t, err)
				assert.True(t, errors.As(err, &refused), "a PlainHTTPTokenError: %v", err)
				assert.Contains(t, err.Error(), "not this computer")
				assert.Contains(t, err.Error(), "https://")
				assert.Empty(t, rec.got, "nothing was sent")
				return
			}
			require.NoError(t, err)
			if !c.wantSent {
				assert.Empty(t, rec.got)
				return
			}
			require.Len(t, rec.got, 1)
			assert.Equal(t, c.wantToken, rec.got[0].Header.Get(SessionTokenHeader))
		})
	}
}

// A token a caller set on the request itself is refused the same way: the header never goes out over plain http off this
// computer, whoever put it there.
func TestACallerSetTokenIsRefusedOverPlainHTTPToo(t *testing.T) {
	prev := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) { return nil, nil }
	t.Cleanup(func() { interfaceAddrs = prev })
	t.Setenv("CORAL_URL", "http://203.0.113.7:8450")
	t.Setenv("CORAL_HOST", "")
	rec := &recorder{}
	client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", hosts: configuredHosts()}}
	req, _ := http.NewRequest("GET", "http://203.0.113.7:8450/api/board", nil)
	req.Header.Set(SessionTokenHeader, "set-by-caller")
	_, err := client.Do(req)
	require.Error(t, err)
	assert.Empty(t, rec.got)
}

// The real interface list: this computer's own non-loopback address counts as local, and a TEST-NET address does not.
func TestLocalInterfaceIPOnThisComputer(t *testing.T) {
	assert.False(t, localInterfaceIP("203.0.113.7"), "TEST-NET-3 is no interface of this computer")
	assert.False(t, localInterfaceIP("coral.lan"), "a name is never local")
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			assert.True(t, localInterfaceIP(n.IP.String()), "this computer's %s", n.IP)
			return
		}
	}
	t.Skip("no non-loopback IPv4 address on this computer")
}
