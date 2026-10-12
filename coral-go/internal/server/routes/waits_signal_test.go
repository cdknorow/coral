package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The long-poll wakes on the store's wait signal, not on a timer: with the
// safety re-check pushed out to an hour, a matching post, a cancel and the
// wait's own expiry still end the poll promptly.
func TestPollWaitWakesOnWaitChanges(t *testing.T) {
	prev := waitPollRecheck
	waitPollRecheck = time.Hour
	t.Cleanup(func() { waitPollRecheck = prev })
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	project := "wait-signal"
	for _, s := range [][3]string{{"Worker", "Developer", "tmux-w"}, {"Lead", "Lead", "tmux-l"}} {
		_, err := handler.bs.Subscribe(ctx, project, s[0], s[1], s[2], nil, nil, "all")
		require.NoError(t, err)
	}
	poll := func(timeout int) (chan map[string]any, time.Time) {
		out := make(chan map[string]any, 1)
		go func() {
			resp, err := http.Get(fmt.Sprintf("%s/api/board/%s/waits/poll?subscriber_id=Worker&timeout=%d", server.URL, project, timeout))
			if err != nil {
				out <- map[string]any{"error": err.Error()}
				return
			}
			defer resp.Body.Close()
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			out <- body
		}()
		time.Sleep(100 * time.Millisecond) // the poll is now asleep
		return out, time.Now()
	}
	within := func(out chan map[string]any, since time.Time, d time.Duration) map[string]any {
		t.Helper()
		select {
		case body := <-out:
			require.Less(t, time.Since(since), d)
			return body
		case <-time.After(d):
			t.Fatalf("poll did not wake within %s", d)
			return nil
		}
	}

	// A matching post resolves it.
	_, err := handler.bs.RegisterWait(ctx, project, "Worker", "tmux-w", "message", "Lead", "", time.Hour)
	require.NoError(t, err)
	out, at := poll(30)
	body, _ := json.Marshal(map[string]string{"subscriber_id": "Lead", "content": "done"})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/messages", server.URL, project), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, "resolved", within(out, at, time.Second)["status"])

	// A cancel wakes it.
	_, err = handler.bs.RegisterWait(ctx, project, "Worker", "tmux-w", "message", "Lead", "", time.Hour)
	require.NoError(t, err)
	out, at = poll(30)
	require.NoError(t, handler.bs.CancelActiveWait(ctx, project, "Worker"))
	require.Equal(t, "resolved", within(out, at, time.Second)["status"])

	// The poll's own deadline still answers "timeout".
	_, err = handler.bs.RegisterWait(ctx, project, "Worker", "tmux-w", "message", "Lead", "", time.Hour)
	require.NoError(t, err)
	out, at = poll(1)
	require.Equal(t, "timeout", within(out, at, 2*time.Second)["status"])
}
