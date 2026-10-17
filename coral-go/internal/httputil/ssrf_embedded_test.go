package httputil

import (
	"net"
	"testing"
)

// #4297: an IPv6 address that embeds an IPv4 one (NAT64, 6to4) is as blocked as that IPv4 address.
func TestEmbeddedIPv4IsHeldToTheIPv4Rules(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		blocked bool
	}{
		{"64:ff9b::7f00:1", true},       // NAT64 of 127.0.0.1
		{"64:ff9b::127.0.0.1", true},    // the same, dotted
		{"64:ff9b::a00:1", true},        // 10.0.0.1
		{"64:ff9b::c0a8:101", true},     // 192.168.1.1
		{"64:ff9b::a9fe:a9fe", true},    // 169.254.169.254, the metadata address
		{"64:ff9b::6440:1", true},       // 100.64.0.1, CGNAT
		{"64:ff9b:1::1", true},          // local-use NAT64
		{"2002:7f00:1::1", true},        // 6to4 of 127.0.0.1
		{"2002:a00:1::", true},          // 10.0.0.1
		{"2002:c0a8:101::5", true},      // 192.168.1.1
		{"2002:a9fe:a9fe::1", true},     // 169.254.169.254
		{"64:ff9b::5db8:d822", false},   // 93.184.216.34, public
		{"2002:5db8:d822::1", false},    // 93.184.216.34, public
		{"2606:4700:4700::1111", false}, // an ordinary public IPv6 address
		{"93.184.216.34", false},
		{"127.0.0.1", true},
	} {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test address %q", tc.ip)
		}
		if got := isIPBlocked(ip); got != tc.blocked {
			t.Errorf("isIPBlocked(%s) = %v, want %v", tc.ip, got, tc.blocked)
		}
	}
}
