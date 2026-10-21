package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// TestMeasureAgentRequests is the #4303 measurement, not a check: it runs only with
// CORAL_MEASURE=1. Agents' board requests (a token, a board label) run concurrently while
// something else writes to the database, and it reports the requests' p50 and p90 and how long
// and how often anything waited for the single write connection. Run it on the commit before the
// change and on the change, on a throwaway data folder.
func TestMeasureAgentRequests(t *testing.T) {
	if os.Getenv("CORAL_MEASURE") != "1" {
		t.Skip("set CORAL_MEASURE=1")
	}
	srv, ts, _ := strictServer(t, true)
	ctx := context.Background()
	const agents, perAgent = 8, 400
	tokens := make([]string, agents)
	for i := range tokens {
		name := fmt.Sprintf("claude-a%d", i)
		tok, err := srv.db.MintSessionToken(ctx, fmt.Sprintf("sid-%d", i), name)
		require.NoError(t, err)
		tokens[i] = tok
		_, err = srv.boardStore.Subscribe(ctx, "team", fmt.Sprintf("A%d", i), "Developer", name, nil, "")
		require.NoError(t, err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the rest of the server's writes: statuses, events, metrics
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = srv.db.ExecContext(ctx, `INSERT OR REPLACE INTO user_settings (key, value) VALUES (?, ?)`, "measure", fmt.Sprint(i))
			time.Sleep(200 * time.Microsecond)
		}
	}()

	before := srv.db.DB.Stats()
	var mu sync.Mutex
	var lat []time.Duration
	start := time.Now()
	var rw sync.WaitGroup
	for i := 0; i < agents; i++ {
		rw.Add(1)
		go func(i int) {
			defer rw.Done()
			for n := 0; n < perAgent; n++ {
				req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/board/team/messages/check?subscriber_id=A%d", ts.URL, i), nil)
				req.Header.Set(store.SessionTokenHeader, tokens[i])
				t0 := time.Now()
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}
		}(i)
	}
	rw.Wait()
	elapsed := time.Since(start)
	close(stop)
	wg.Wait()
	after := srv.db.DB.Stats()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("MEASURE requests=%d elapsed=%v rps=%.0f p50=%v p90=%v p99=%v writer_waits=%d writer_wait_total=%v",
		len(lat), elapsed.Round(time.Millisecond), float64(len(lat))/elapsed.Seconds(), lat[len(lat)/2], lat[len(lat)*9/10], lat[len(lat)*99/100],
		after.WaitCount-before.WaitCount, (after.WaitDuration - before.WaitDuration).Round(time.Microsecond))
}
