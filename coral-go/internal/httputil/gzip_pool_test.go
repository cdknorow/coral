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
