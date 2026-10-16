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

	"github.com/cdknorow/coral/internal/secrets"
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
		frames := len(dst.got())
		start := time.Now()
		require.NoError(t, w.write(websocket.MessageBinary, []byte(text[i:i+1])))
		// The key has left when a frame went out for it (the key itself, or the marker when it
		// completed a token).
		for len(dst.got()) == frames && time.Since(start) < timeout {
			time.Sleep(time.Millisecond)
		}
		lat = append(lat, time.Since(start))
		if dst.binary() != shown { // nothing is held, so a difference is a completed token shown as the marker
			shown = dst.binary()
		}
	}
	return lat
}

// Typing through the production detector is echoed at once, key by key: the guard for #3958. Nothing is held (the
// operator's decision), so each key's echo goes out within echoBound of its own frame, ordinary commands and a typed
// token alike (its completing key goes out as the marker).
func TestWSRedactTypingEchoIsNotHeldBack(t *testing.T) {
	for _, cmd := range []string{"git status", "ls -la", "cd /tmp", "make test", "echo hello world", "kubectl get pods", "cat README.md", "python3 -m pytest"} {
		t.Run(cmd, func(t *testing.T) {
			on := atomic.Bool{}
			on.Store(true)
			dst := &wsRecorder{}
			w := newWSRedact(context.Background(), dst, secrets.Redactor(), on.Load, true)
			defer w.close()
			lat := typeKeys(t, w, dst, "$ ", cmd, 300*time.Millisecond)
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

	// The operator's decision for #3958: nothing on the terminal stream is held, so every key leaves at once, a token's
	// keys included (a secret split across frames can show in part: the accepted risk, recorded in the next test).
	t.Run("every key leaves at once, a typed token's too", func(t *testing.T) {
		for _, tok := range []string{fake("ghp_", 36, alnum), "AKIA" + fake("", 16, upper), fake("sk-", 40, alnum)} {
			on := atomic.Bool{}
			on.Store(true)
			dst := &wsRecorder{}
			w := newWSRedact(context.Background(), dst, secrets.Redactor(), on.Load, true)
			lat := typeKeys(t, w, dst, "$ export T=", tok, 300*time.Millisecond)
			for i, d := range lat {
				assert.LessOrEqual(t, d, echoBound, "key %d (%q) of a typed %s... token left within %v, was %v", i, tok[i], tok[:4], echoBound, d)
			}
			w.close()
		}
	})
}

// A secret that arrives whole in one frame is replaced, with or without a prompt before it in the same frame.
func TestWSRedactOneFrameSecretIsReplaced(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	on := atomic.Bool{}
	on.Store(true)
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, secrets.Redactor(), on.Load, true)
	defer w.close()
	require.NoError(t, w.write(websocket.MessageBinary, []byte("$ echo "+tok+"\r\n")))
	assert.Equal(t, "$ echo "+redact.Marker+"\r\n", dst.binary())
	assert.NotContains(t, dst.binary(), tok[4:])
}

// ACCEPTED RISK (operator, #3958): the stream holds nothing, so a secret split across frames (typed key by key, or cut at a
// frame boundary) can show in part, or in full, to a Hub viewer. This test documents that behaviour so a change to it is a
// decision, not an accident; it asserts only what the operator accepted: the output arrives at once and nothing is lost.
func TestWSRedactSplitFrameSecretIsTheAcceptedRisk(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	on := atomic.Bool{}
	on.Store(true)
	dst := &wsRecorder{}
	w := newWSRedact(context.Background(), dst, secrets.Redactor(), on.Load, true)
	defer w.close()
	require.NoError(t, w.write(websocket.MessageBinary, []byte("$ export T="+tok[:20])))
	require.NoError(t, w.write(websocket.MessageBinary, []byte(tok[20:]+"\r\n")))
	got := dst.binary()
	assert.True(t, strings.HasPrefix(got, "$ export T="), "the output arrived: %q", got)
	assert.Contains(t, got, tok[:20], "the first frame's part of the split secret shows (accepted risk, #3958)")
}
