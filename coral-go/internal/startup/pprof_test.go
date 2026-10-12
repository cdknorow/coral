package startup

import "testing"

func TestCheckLoopbackAddr(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:6060", "[::1]:6060", "127.0.0.2:0"} {
		if err := checkLoopbackAddr(ok); err != nil {
			t.Errorf("%s: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "192.168.1.5:6060", "localhost:6060", "example.com:6060", "127.0.0.1"} {
		if err := checkLoopbackAddr(bad); err == nil {
			t.Errorf("%s: accepted, want refused", bad)
		}
	}
}
