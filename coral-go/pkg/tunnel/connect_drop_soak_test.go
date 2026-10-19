package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// openFDs is how many file descriptors this process holds (-1 where /dev/fd is not there).
func openFDs() int {
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

// A hub connection that streams a terminal and an event stream and is then dropped by the network, 200 times over (#5160: the long-uptime
// audit's hub tunnel case): the connector reconnects on its own each time, and nothing it set up for a dead connection stays behind: no goroutine
// and no file descriptor. (Heap is not asserted: a stream that was open at the drop is held by yamux's 30 s StreamCloseTimeout timer, about 10 KB each,
// and is freed after it; the soak measured the heap back at its baseline 33 s after 400 drops, so it is a delay, not a leak.)
func TestHubConnectStreamDropRepeatedlyLeavesNothingBehind(t *testing.T) {
	cycles := 200
	if testing.Short() {
		cycles = 20
	}
	hub := newHub(t, fastCfg())
	proxy := newFlakyProxy(t, strings.TrimPrefix(hub.Server.URL, "http://"))
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			ctx := c.CloseRead(r.Context()) // a terminal reads its input; a server that never reads cannot answer a close
			for i := 0; ; i++ {
				if err := c.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf("frame %d", i))); err != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ls.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, "ws://"+proxy.Addr(), testToken, ClientOptions{
			SessionConfig: fastCfg(), Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll, Redactor: redact.Rules}, AllowInsecure: true,
			Backoff: Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond},
		})
	}()

	cycle := func() {
		t.Helper()
		// hub.next would keep every session alive in the test's cleanup list, which is what a heap measure must not include.
		var sess *Session
		select {
		case sess = <-hub.Sessions:
		case <-time.After(5 * time.Second):
			t.Fatal("no session")
		}
		defer sess.Close()
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		ws, err := sess.DialWS(rctx, "/ws/terminal/a", "", nil)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			_, _, err = ws.Read(rctx)
			require.NoError(t, err)
		}
		resp := do(t, sess, "GET", "/api/events", "", nil, nil)
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "data: 1\n", line)

		proxy.Drop() // the network fails with both streams open
		select {
		case <-sess.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the hub session did not end")
		}
		ws.CloseNow()
		resp.Body.Close()
	}
	for i := 0; i < 10; i++ { // warm up: pools, lazy goroutines
		cycle()
	}
	time.Sleep(300 * time.Millisecond)
	gBefore, fBefore := goroutines(), openFDs()
	for i := 0; i < cycles; i++ {
		cycle()
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	proxy.Drop()
	require.Eventually(t, func() bool { return goroutines() <= gBefore+6 }, 10*time.Second, 50*time.Millisecond,
		"goroutines before=%d after=%d", gBefore, goroutines())
	if fBefore >= 0 {
		require.Eventually(t, func() bool { return openFDs() <= fBefore+6 }, 10*time.Second, 50*time.Millisecond,
			"file descriptors before=%d after=%d", fBefore, openFDs())
	}
	t.Logf("after %d drops: goroutines %d -> %d, fds %d -> %d", cycles, gBefore, goroutines(), fBefore, openFDs())
}
