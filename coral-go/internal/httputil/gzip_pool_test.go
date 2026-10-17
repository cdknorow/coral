package httputil

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gzipReply(body string) http.Handler {
	return GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
}

// #4028: the gzip writers are pooled; replies served at the same time each decode to their own
// body (a pooled writer never carries one reply into another).
func TestGzipAPIPooledWritersKeepRepliesApart(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"n":%d,"pad":%q}`, i, strings.Repeat(fmt.Sprint(i), GzipAPIMinBytes))
			r := httptest.NewRequest("GET", "/api/x", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			gzipReply(body).ServeHTTP(w, r)
			assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
			zr, err := gzip.NewReader(w.Body)
			require.NoError(t, err)
			got, err := io.ReadAll(zr)
			require.NoError(t, err)
			assert.Equal(t, body, string(got))
		}()
	}
	wg.Wait()
}

func BenchmarkGzipAPI(b *testing.B) {
	h := gzipReply(`{"pad":"` + strings.Repeat("abcdefgh", 2048) + `"}`)
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
}

// drainGzipWriters empties the free list, so a test sees only what its own replies put back.
func drainGzipWriters() {
	for {
		select {
		case <-gzipWriters:
		default:
			return
		}
	}
}

func gzipRequest() *http.Request {
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	return r
}

// #4455: the free list survives garbage collection (a sync.Pool was emptied at each one and refilled
// at 814 KB a writer, #4446): after a GC a reply reuses the kept writer instead of making one.
func TestGzipWritersSurviveGC(t *testing.T) {
	drainGzipWriters()
	h := gzipReply(`{"pad":"` + strings.Repeat("abcdefgh", 512) + `"}`)
	h.ServeHTTP(httptest.NewRecorder(), gzipRequest())
	require.Len(t, gzipWriters, 1, "the writer is kept after a whole reply")
	runtime.GC()
	runtime.GC()
	require.Len(t, gzipWriters, 1, "and still there after two collections")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	h.ServeHTTP(httptest.NewRecorder(), gzipRequest())
	runtime.ReadMemStats(&after)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(400<<10), "no new 814 KB writer for the reply")
}

// At most gzipIdleWriters writers are kept, however many replies ran at once.
func TestGzipWritersAreBounded(t *testing.T) {
	drainGzipWriters()
	start, release := make(chan struct{}), make(chan struct{})
	h := GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"pad":"`+strings.Repeat("x", 2*GzipAPIMinBytes)+`"}`)
		start <- struct{}{}
		<-release // every reply holds its writer at the same time
	}))
	var wg sync.WaitGroup
	for i := 0; i < 3*gzipIdleWriters; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), gzipRequest()) }()
		<-start
	}
	close(release)
	wg.Wait()
	assert.Len(t, gzipWriters, gzipIdleWriters)
}

// failingWriter accepts the header and fails every write: the client went away mid-reply.
type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) WriteHeader(int)           {}
func (f *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// A reply that failed to write, or whose handler panicked, never puts its writer back.
func TestGzipWriterNotKeptAfterAFailedOrPanickedReply(t *testing.T) {
	drainGzipWriters()
	big := `{"pad":"` + strings.Repeat("y", 4*GzipAPIMinBytes) + `"}`
	gzipReply(big).ServeHTTP(&failingWriter{h: http.Header{}}, gzipRequest())
	assert.Empty(t, gzipWriters, "a reply the client didn't take")

	h := GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, big) // compression started: the writer was taken
		panic("boom")
	}))
	func() {
		defer func() { assert.NotNil(t, recover(), "the panic goes on to the Recoverer") }()
		h.ServeHTTP(httptest.NewRecorder(), gzipRequest())
	}()
	assert.Empty(t, gzipWriters, "a reply whose handler panicked")
}
