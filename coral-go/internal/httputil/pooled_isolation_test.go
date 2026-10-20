package httputil

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// body returns what a client would read from the recorded reply, gunzipped when it was sent gzipped.
func replyText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Header().Get("Content-Encoding") != "gzip" {
		return w.Body.String()
	}
	zr, err := gzip.NewReader(w.Body)
	require.NoError(t, err)
	b, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(b)
}

// #5353: two different users' replies, interleaved on the shared gzip writers and JSON buffers (gzipped and plain, long and short, through both
// writers, from many goroutines; run with -race): each reply holds its own user's marker and never a byte of the other's.
func TestTwoUsersRepliesInterleavedNeverShareABytePooled(t *testing.T) {
	const aSecret, bSecret = "ALICE-SECRET-7f3a", "BOB-SECRET-91c2"
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			mine, other := aSecret, bSecret
			if g%2 == 1 {
				mine, other = bSecret, aSecret
			}
			for i := 0; i < 150; i++ {
				v := map[string]any{"owner": mine, "i": i}
				switch i % 3 {
				case 0: // long, so the buffer and the compressor are well used
					v["rows"] = strings.Repeat(mine+",", 4<<10)
				case 1: // just over the gzip threshold
					v["rows"] = strings.Repeat(mine, gzipMinBytes/len(mine)+1)
				} // else short
				for name, write := range map[string]func(http.ResponseWriter, *http.Request, int, any){"validated": WriteJSONValidated, "compressed": WriteJSONCompressed} {
					r := httptest.NewRequest("GET", "/x", nil)
					if (g+i)%2 == 0 {
						r.Header.Set("Accept-Encoding", "gzip")
					}
					w := httptest.NewRecorder()
					write(w, r, 200, v)
					text := replyText(t, w)
					if !assert.Contains(t, text, mine, "%s g%d i%d lost its own data", name, g, i) || !assert.NotContains(t, text, other, "%s g%d i%d carries the other user's data", name, g, i) {
						return
					}
					if w.Header().Get("Content-Encoding") != "gzip" {
						assert.Equal(t, fmt.Sprint(w.Body.Len()), w.Header().Get("Content-Length"), name)
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

// panicWriter is a ResponseWriter whose body write panics (what a failed hub redactor or a middleware can do): the pooled buffer is handed back on the
// way out of the panic, and must come back empty and unshared.
type panicWriter struct{ *httptest.ResponseRecorder }

func (panicWriter) Write([]byte) (int, error) { panic("write failed") }

func TestAPanicOrAnEncodeFailureLeavesNothingForTheNextReply(t *testing.T) {
	const secret = "PANIC-REPLY-SECRET"
	big := map[string]any{"rows": strings.Repeat(secret, 2<<10)}
	for name, write := range map[string]func(http.ResponseWriter, *http.Request, int, any){"validated": WriteJSONValidated, "compressed": WriteJSONCompressed} {
		for _, gz := range []bool{false, true} {
			r := httptest.NewRequest("GET", "/x", nil)
			if gz {
				r.Header.Set("Accept-Encoding", "gzip")
			}
			func() {
				defer func() { _ = recover() }()
				write(panicWriter{httptest.NewRecorder()}, r, 200, big)
			}()
			// An encode failure after some bytes were already in the buffer.
			w := httptest.NewRecorder()
			write(w, r, 200, map[string]any{"ok": secret, "bad": make(chan int)})
			assert.Equal(t, http.StatusInternalServerError, w.Code, name)

			// The next replies, many of them so every pooled buffer is used, hold nothing of the failed ones.
			for i := 0; i < 20; i++ {
				w := httptest.NewRecorder()
				write(w, httptest.NewRequest("GET", "/x", nil), 200, map[string]any{"n": i})
				assert.JSONEq(t, fmt.Sprintf(`{"n":%d}`, i), w.Body.String(), "%s after a failure", name)
			}
		}
	}
}
