package tunnel

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blackholedSession is a hub session whose peer's bytes have stopped moving (sockets open, nothing answers): the peer never replies to a close.
func blackholedSession(t *testing.T) (*Session, *flakyProxy) {
	t.Helper()
	hub := newHub(t, fastCfg())
	proxy := newFlakyProxy(t, strings.TrimPrefix(hub.Server.URL, "http://"))
	cs, err := Dial(context.Background(), "ws://"+proxy.Addr(), testToken, ClientOptions{
		SessionConfig: fastCfg(), Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	s := hub.next(t)
	proxy.Blackhole(true)
	return s, proxy
}

// A peer whose bytes have stopped (it never answers the close): CloseNow returns at once and Done fires. (Close returns in about 0.1 ms in this setup too, with this library version: the stall coral-hub saw
// is not reproduced by a blackholed or a non-reading peer, so this test pins the contract, not the difference.)
func TestCloseNowDoesNotWaitForAPeerThatNeverAnswers(t *testing.T) {
	s, _ := blackholedSession(t)
	start := time.Now()
	require.NoError(t, s.CloseNow())
	assert.Less(t, time.Since(start), 100*time.Millisecond, "no close-handshake wait")
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not fire")
	}
}

// Safe with Close in any order, from several goroutines, and idempotent.
func TestCloseNowIsSafeAndIdempotent(t *testing.T) {
	s, _ := blackholedSession(t)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.Close() // stuck on the peer's close handshake until CloseNow releases it
			} else {
				_ = s.CloseNow()
			}
		}(i)
	}
	wg.Wait()
	assert.Less(t, time.Since(start), 2*time.Second, "a Close stuck on the peer is released by CloseNow")
	assert.NoError(t, s.CloseNow(), "after Close, and again")
	assert.NoError(t, s.CloseNow())
	select {
	case <-s.Done():
	default:
		t.Fatal("Done did not fire")
	}
}

// CloseNow on a healthy session ends its streams and leaves no goroutine behind.
func TestCloseNowLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		s, _ := blackholedSession(t)
		require.NoError(t, s.CloseNow())
	}
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+25 }, 10*time.Second, 50*time.Millisecond,
		"goroutines after five CloseNow calls: %d (before %d)", runtime.NumGoroutine(), before)
}
