package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6318 (coral-hub's ask): a hub busy with a reconnect storm answers retry_later with retry_after (whole seconds, at most MaxRetryAfter), which is the floor of the connector's next wait;
// and the first reconnect after a session that was up is spread evenly over ten seconds instead of about one. An old hub sends no field and nothing changes.

func dialRefused(t *testing.T, auth AuthFunc) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { AcceptHTTP(w, r, auth, fastCfg()) }))
	defer srv.Close()
	_, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "t", ClientOptions{
		Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true,
	})
	return err
}

func TestARetryAfterRidesTheRefusalAndIsCapped(t *testing.T) {
	for name, c := range map[string]struct {
		auth AuthFunc
		want time.Duration
	}{
		"a hub that asks for 42 s":          {func(context.Context, string) (any, error) { return nil, RetryLater(42 * time.Second) }, 42 * time.Second},
		"a hub that asks for an hour":       {func(context.Context, string) (any, error) { return nil, RetryLater(time.Hour) }, MaxRetryAfter},
		"a hub that asks for nothing":       {func(context.Context, string) (any, error) { return nil, RetryLater(0) }, 0},
		"an old hub (the plain refusal)":    {func(context.Context, string) (any, error) { return nil, ErrRetryLater }, 0},
		"an old hub (the auth outage word)": {func(context.Context, string) (any, error) { return nil, ErrAuthUnavailable }, 0},
		"half a second is not a second":     {func(context.Context, string) (any, error) { return nil, RetryLater(500 * time.Millisecond) }, 0},
	} {
		err := dialRefused(t, c.auth)
		require.ErrorIs(t, err, ErrRetryLater, name+": it is still the temporary refusal")
		assert.Equal(t, c.want, RetryAfterOf(err), name)
	}
	assert.Equal(t, ErrRetryLater, dialRefused(t, func(context.Context, string) (any, error) { return nil, ErrRetryLater }), "a refusal with no wait is ErrRetryLater itself, as before")
	assert.Equal(t, time.Duration(0), RetryAfterOf(ErrUnauthorized))
	assert.Equal(t, time.Duration(0), RetryAfterOf(nil))
}

// A hub (or a peer pretending to be one) cannot park a connector for longer than the cap, or with a negative number.
func TestAHostileRetryAfterIsClamped(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		-5 * time.Second: 0, 0: 0, 3 * time.Second: 3 * time.Second, MaxRetryAfter: MaxRetryAfter, MaxRetryAfter + time.Second: MaxRetryAfter, 1<<62 - 1: MaxRetryAfter,
	} {
		assert.Equal(t, want, clampRetryAfter(in), in)
	}
}

func TestTheNextDelayIsNeverBelowTheHubsRetryAfter(t *testing.T) {
	b := Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond}
	for i := 0; i < 200; i++ {
		assert.GreaterOrEqual(t, b.nextDelay(3, false, 2*time.Second), 2*time.Second, "the floor holds over the backoff")
		assert.GreaterOrEqual(t, b.nextDelay(0, true, 2*time.Second), 2*time.Second, "and over the first reconnect's spread")
	}
	for i := 0; i < 200; i++ {
		assert.LessOrEqual(t, b.nextDelay(3, false, 0), 7*time.Millisecond, "no floor: the backoff as it was (Max with its +25%)")
	}
	long := Backoff{}
	assert.Equal(t, 300*time.Second, long.nextDelay(20, false, MaxRetryAfter), "a floor above the backoff's own Max is honoured: that is its point")
}

// The first reconnect after a session that was up is spread over 0 to 10 s with the defaults: a restarting hub does not get every connector back in the same second.
func TestTheFirstReconnectIsSpreadOverTenSeconds(t *testing.T) {
	b := Backoff{}
	var below1, above5 int
	for i := 0; i < 2000; i++ {
		d := b.nextDelay(0, true, 0)
		require.GreaterOrEqual(t, d, time.Duration(0))
		require.Less(t, d, 10*time.Second)
		if d < time.Second {
			below1++
		}
		if d > 5*time.Second {
			above5++
		}
	}
	assert.Greater(t, below1, 0, "some come back at once")
	assert.Greater(t, above5, 500, "about half wait more than five seconds: not clustered around one second as 1 s +-25% was")

	// An ordinary retry is the backoff as before: 1 s +-25% for the first, never uniform.
	for i := 0; i < 500; i++ {
		d := b.nextDelay(0, false, 0)
		assert.GreaterOrEqual(t, d, 750*time.Millisecond)
		assert.LessOrEqual(t, d, 1250*time.Millisecond)
	}
	// Small configured delays keep the spread small (a test's backoff stays fast); an explicit window is honoured.
	small := Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond}
	for i := 0; i < 200; i++ {
		assert.Less(t, small.nextDelay(0, true, 0), 5*time.Millisecond)
	}
	w := Backoff{FirstJitter: 40 * time.Millisecond}
	for i := 0; i < 200; i++ {
		assert.Less(t, w.nextDelay(0, true, 0), 40*time.Millisecond)
	}
}

// Run honours the hub's retry_after: the second dial comes no sooner than the wait the first refusal asked for, and a refusal with none is retried at the backoff's pace.
func TestRunWaitsForTheHubsRetryAfter(t *testing.T) {
	for name, c := range map[string]struct {
		refusal      error
		min, max     time.Duration
		wantTwoCalls bool
	}{
		"retry_after 1 s":      {RetryLater(time.Second), 900 * time.Millisecond, 4 * time.Second, true},
		"an old hub, no field": {ErrRetryLater, 0, 600 * time.Millisecond, true},
	} {
		var mu sync.Mutex
		var at []time.Time
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			AcceptHTTP(w, r, func(context.Context, string) (any, error) {
				mu.Lock()
				at = append(at, time.Now())
				n := len(at)
				mu.Unlock()
				if n == 1 {
					return nil, c.refusal
				}
				return "id", nil
			}, fastCfg())
		}))
		ctx, cancel := context.WithCancel(context.Background())
		up := make(chan struct{}, 1)
		go Run(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "t", ClientOptions{
			Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true, SessionConfig: fastCfg(),
			Backoff: Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond},
			OnState: func(connected bool, err error) {
				if connected {
					select {
					case up <- struct{}{}:
					default:
					}
				}
			},
		})
		select {
		case <-up:
		case <-time.After(8 * time.Second):
			t.Fatal(name + ": never connected")
		}
		cancel()
		srv.Close()
		mu.Lock()
		require.GreaterOrEqual(t, len(at), 2, name)
		gap := at[1].Sub(at[0])
		mu.Unlock()
		assert.GreaterOrEqual(t, gap, c.min, name)
		assert.Less(t, gap, c.max, name)
	}
}
