package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The deadline is rolling: a reply written in ONE Write call (a changes.diff, a media preview, a JSON body to a client that does
// not take gzip) to a client that reads steadily but slower than the deadline would allow for the whole buffer is not cut.
func TestWriteDeadlineRollsAcrossOneBigWrite(t *testing.T) {
	const limit = 300 * time.Millisecond
	const size = 48 << 20
	var writeErr error
	srv := httptest.NewServer(WriteDeadlineAfter(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, writeErr = w.Write(make([]byte, size)) // one call
	})))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	start := time.Now()
	buf := make([]byte, 1<<20)
	got := 0
	for {
		n, err := resp.Body.Read(buf)
		got += n
		if err != nil {
			break
		}
		time.Sleep(time.Millisecond) // steady, but the whole buffer takes far longer than the deadline
	}
	assert.Greater(t, time.Since(start), 2*limit, "the transfer outlasted the per-write deadline")
	assert.Equal(t, size, got, "all of it arrived")
	assert.NoError(t, writeErr)
}

// What is written is what arrives, whatever the size: the chunking changes nothing the client sees.
func TestWriteDeadlineChunkingKeepsTheBytes(t *testing.T) {
	for _, size := range []int{0, 1, writeChunk - 1, writeChunk, writeChunk + 1, 3*writeChunk + 17} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i * 7)
		}
		srv := httptest.NewServer(WriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, err := w.Write(payload)
			assert.NoError(t, err)
			assert.Equal(t, size, n, "Write reports the whole length")
		})))
		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		srv.Close()
		require.NoError(t, err)
		assert.Equal(t, payload, got, "size %d", size)
	}
}
