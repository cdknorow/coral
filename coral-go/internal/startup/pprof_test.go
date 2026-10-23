//go:build dev

package startup

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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

// #3262 B: with CORAL_PPROF_ADDR the profiles are served on their own loopback listener
// only; a non-loopback address never starts one.
func TestPprofListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := startPprofOn(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr.String() + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "goroutine profile") {
		t.Fatalf("pprof listener: %d %.80s", resp.StatusCode, b)
	}
	for _, bad := range []string{"0.0.0.0:0", "[::]:0", ":0"} {
		if a, err := startPprofOn(ctx, bad); err == nil {
			t.Errorf("%s: started on %v, want refused", bad, a)
		}
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := http.Get("http://" + addr.String() + "/debug/pprof/"); err == nil {
		t.Error("still serving after shutdown")
	}
}
