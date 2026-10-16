package tunnel

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// echoBound is how long a typed key's echo may take to leave the stream when nothing token-like is pending (#3959).
// The echo of a key is one tiny binary frame; the redaction work is microseconds, so anything near the 100 ms idle
// flush (or the 1 s hold) means the stream is holding ordinary typing back, which is what made the terminal through
// Coral Hub feel slow on v1.4.61 (#3958).
const echoBound = 50 * time.Millisecond

// typeKeys writes `text` one byte per frame, as a terminal echoes typing, and returns each key's latency: the time from
// the write until everything typed so far has left the stream (or the timeout, if it never does).
func typeKeys(t *testing.T, w *wsRedact, dst *wsRecorder, prefix, text string, timeout time.Duration) (lat []time.Duration) {
	t.Helper()
	require.NoError(t, w.write(websocket.MessageBinary, []byte(prefix)))
	shown := prefix
	for i := 0; i < len(text); i++ {
		shown += text[i : i+1]
		start := time.Now()
		require.NoError(t, w.write(websocket.MessageBinary, []byte(text[i:i+1])))
		for dst.binary() != shown && time.Since(start) < timeout {
			time.Sleep(time.Millisecond)
		}
		lat = append(lat, time.Since(start))
		if dst.binary() != shown { // never came: the later keys cannot be told from this one, so settle it and go on
			w.mu.Lock()
			_ = redactFlush(w.red, true)
			_ = w.sendOut()
			w.mu.Unlock()
			shown = dst.binary()
		}
	}
	return lat
}

// Typing ordinary commands through the real detector is echoed at once, key by key, while a token typed the same way is
// still held: the guard for #3958. The words below contain no token-like run (no 8-character mix of letters and digits,
// no secret prefix followed by a value), so each key's echo must go out within echoBound of its own frame, not after the
// stream's idle flush. A token-like tail (a secret prefix and the start of its value) must still be held back.
func TestWSRedactTypingEchoIsNotHeldBack(t *testing.T) {
	for _, cmd := range []string{"git status", "ls -la", "cd /tmp", "make test", "echo hello world", "kubectl get pods", "cat README.md", "python3 -m pytest"} {
		t.Run(cmd, func(t *testing.T) {
			on := atomic.Bool{}
			on.Store(true)
			dst := &wsRecorder{}
			w := newWSRedact(context.Background(), dst, redact.Rules, on.Load, true)
			defer w.close()
			lat := typeKeys(t, w, dst, "$ ", cmd, 2*time.Second)
			var slow []string
			for i, d := range lat {
				if d > echoBound {
					slow = append(slow, fmt.Sprintf("%q after %v", cmd[i], d.Round(time.Millisecond)))
				}
			}
			assert.Empty(t, slow, "each key's echo is forwarded within %v: these were held: %s", echoBound, strings.Join(slow, ", "))
			assert.Equal(t, "$ "+cmd, dst.binary(), "and every key arrived")
		})
	}

	t.Run("a token typed key by key is still held", func(t *testing.T) {
		tok := fake("ghp_", 36, alnum)
		on := atomic.Bool{}
		on.Store(true)
		dst := &wsRecorder{}
		w := newWSRedact(context.Background(), dst, redact.Rules, on.Load, true)
		defer w.close()
		require.NoError(t, w.write(websocket.MessageBinary, []byte("$ export T=")))
		for i := 0; i < 16; i++ { // the prefix and 12 characters of the value
			require.NoError(t, w.write(websocket.MessageBinary, []byte(tok[i:i+1])))
		}
		time.Sleep(echoBound * 3) // longer than the echo bound: a value-bearing tail must still be held
		assert.NotContains(t, dst.binary(), tok[4:], "no character of the value is shown while it is typed")
		assert.NotContains(t, dst.binary(), tok[:4]+tok[4:5], "and the prefix is not released with a value start after it")
		require.NoError(t, w.write(websocket.MessageBinary, []byte(tok[16:]+"\r\n")))
		assert.NotContains(t, dst.binary(), tok[4:], "the finished token never appears")
		assert.Contains(t, dst.binary(), redact.Marker, "it is replaced by the marker")
	})
}
