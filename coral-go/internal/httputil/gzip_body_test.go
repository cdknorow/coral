package httputil

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
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

// A reply of the size of the dashboard's live list: 50 rows of a few hundred bytes each (about 70 KB of JSON).
func livelikeRows() []map[string]any {
	rows := make([]map[string]any, 50)
	for i := range rows {
		rows[i] = map[string]any{"name": "work", "session_id": fmt.Sprintf("6202b83b-ef3e-dec1-d4c8-%012d", i), "display_name": fmt.Sprintf("A%02dt1", i), "agent_type": "claude",
			"first_prompt": strings.Repeat("do the work of item ", 20), "branch": "main", "repo_name": "coral", "staleness_seconds": i, "board_unread": i % 3, "commands": []string{"a", "b", "c"},
			"git_state": map[string]any{"ahead": 1, "behind": 0, "dirty": i%2 == 0}}
	}
	return rows
}

func serveJSON(b *testing.B, write func(http.ResponseWriter, *http.Request, int, any), v any) {
	r := httptest.NewRequest("GET", "/api/sessions/live", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		write(w, r, http.StatusOK, v)
	}
}

// go test ./internal/httputil -run xxx -bench WriteJSON -benchmem
func BenchmarkWriteJSONValidatedCompressed(b *testing.B) {
	serveJSON(b, WriteJSONValidated, livelikeRows())
}
func BenchmarkWriteJSONCompressedCompressed(b *testing.B) {
	serveJSON(b, WriteJSONCompressed, livelikeRows())
}

// #5349: a compressed JSON reply allocates neither a gzip writer's tables (814 KB at BestSpeed) nor a buffer grown by doubling for its JSON: measured
// on a 70 KB answer, 918 KB per reply before, 76 KB with the writers reused, 33 KB with the encode buffers reused too. The 96 KiB bound pins the reuse of the writers (the buffers add a margin for the Windows and Linux runners, 62 to 64 KiB measured, #5349 gate).
func TestCompressedJSONRepliesReuseTheirGzipWriters(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector inflates allocations")
	}
	rows := livelikeRows()
	for name, write := range map[string]func(http.ResponseWriter, *http.Request, int, any){"validated": WriteJSONValidated, "compressed": WriteJSONCompressed} {
		res := testing.Benchmark(func(b *testing.B) { serveJSON(b, write, rows) })
		perOp := res.AllocedBytesPerOp()
		assert.Less(t, perOp, int64(96<<10), "%s: %d bytes allocated per reply (a fresh gzip writer alone is about 830 KB)", name, perOp)
	}
}

// Reused writers never carry one reply into another, and what is sent is valid gzip of exactly the JSON.
func TestPooledGzipRepliesDecodeToTheirOwnBody(t *testing.T) {
	for i := 0; i < 30; i++ {
		v := map[string]any{"n": i, "pad": strings.Repeat(fmt.Sprint(i%10), 3*gzipMinBytes)}
		want, _ := json.Marshal(v)
		for name, write := range map[string]func(http.ResponseWriter, *http.Request, int, any){"validated": WriteJSONValidated, "compressed": WriteJSONCompressed} {
			r := httptest.NewRequest("GET", "/x", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			write(w, r, 200, v)
			require.Equal(t, "gzip", w.Header().Get("Content-Encoding"), name)
			sent := w.Body.Len()
			zr, err := gzip.NewReader(w.Body)
			require.NoError(t, err, name)
			got, err := io.ReadAll(zr)
			require.NoError(t, err, name)
			assert.JSONEq(t, string(want), string(got), "%s reply %d", name, i)
			assert.Equal(t, fmt.Sprint(sent), w.Header().Get("Content-Length"), name+": the length sent is the length said")
		}
	}
}

// Reused buffers are emptied between replies: a short reply after a long one carries nothing of it, concurrently too (run with -race).
func TestPooledJSONBuffersNeverCarryOneReplyIntoAnother(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				v := map[string]any{"g": g, "i": i}
				if i%7 == 0 {
					v["pad"] = strings.Repeat("x", 20<<10) // a long one, so a later short one would show a leftover
				}
				want, _ := json.Marshal(v)
				for _, write := range []func(http.ResponseWriter, *http.Request, int, any){WriteJSONValidated, WriteJSONCompressed} {
					r := httptest.NewRequest("GET", "/x", nil) // no gzip: the body goes out as encoded
					w := httptest.NewRecorder()
					write(w, r, 200, v)
					assert.JSONEq(t, string(want), w.Body.String())
				}
			}
		}(g)
	}
	wg.Wait()
}

// A reply bigger than jsonBufKeep is not kept for reuse.
func TestAHugeJSONBufferIsNotKept(t *testing.T) {
	for len(jsonBufs) > 0 {
		<-jsonBufs
	}
	big := new(bytes.Buffer)
	big.Grow(jsonBufKeep + 1)
	putJSONBuf(big)
	assert.Empty(t, jsonBufs, "a huge buffer is dropped")
	small := new(bytes.Buffer)
	small.WriteString("x")
	putJSONBuf(small)
	require.Len(t, jsonBufs, 1)
	got := getJSONBuf()
	assert.Zero(t, got.Len(), "and a kept one comes back empty")
}
