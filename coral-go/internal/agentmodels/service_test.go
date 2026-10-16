package agentmodels

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCLI is an executable file whose path stands for a family's CLI (the fetcher is fake).
func fakeCLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cli")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755))
	return p
}

func newTestService(t *testing.T, path string, fetch Fetcher) (*Service, *time.Time) {
	t.Helper()
	s := NewService(func(string) (string, error) { return path, nil })
	s.fetch = fetch
	clock := time.Now()
	s.now = func() time.Time { return clock }
	t.Cleanup(s.Wait)
	return s, &clock
}

// Get never waits for a CLI: the first call returns [] and starts a fetch; the next returns the
// list. It is used for maxAge, then fetched again in the background; a new build of the CLI is
// fetched at once.
func TestGetIsCachedAndRefreshedInTheBackground(t *testing.T) {
	path := fakeCLI(t)
	var calls atomic.Int32
	release := make(chan struct{})
	s, clock := newTestService(t, path, func(ctx context.Context, family, p string) ([]Model, string, error) {
		calls.Add(1)
		<-release
		return []Model{{Value: "opus", DisplayName: "Opus"}}, "account", nil
	})
	r := s.Get("claude")
	assert.Equal(t, "builtin", r.Source, "the curated list until the CLI has listed its own")
	assert.True(t, r.Refreshing, "a fetch started")
	assert.True(t, s.Get("claude").Refreshing)
	close(release)
	s.Wait()
	assert.EqualValues(t, 1, calls.Load(), "one fetch for the two calls")
	r = s.Get("claude")
	assert.Equal(t, []string{"opus"}, values(r.Models))
	assert.False(t, r.Refreshing)
	require.NotNil(t, r.FetchedAt)
	assert.Equal(t, "account", r.Source)

	*clock = clock.Add(maxAge - time.Minute)
	s.Get("claude")
	s.Wait()
	assert.EqualValues(t, 1, calls.Load(), "fresh: not fetched again")
	*clock = clock.Add(2 * time.Minute)
	r = s.Get("claude")
	assert.Equal(t, []string{"opus"}, values(r.Models), "the old list while it refreshes")
	s.Wait()
	assert.EqualValues(t, 2, calls.Load(), "old: fetched again")

	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, later, later))
	s.Get("claude")
	s.Wait()
	assert.EqualValues(t, 3, calls.Load(), "a new build of the CLI: fetched at once")
}

// A failing CLI keeps the old list, says why in Coral's words (never the CLI's output), and is
// retried after retryAfterError, not on every call.
func TestFailedFetchIsRetriedLater(t *testing.T) {
	path := fakeCLI(t)
	var calls atomic.Int32
	fail := atomic.Bool{}
	s, clock := newTestService(t, path, func(ctx context.Context, family, p string) ([]Model, string, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, "", errors.New("raw CLI output: secret@example.test")
		}
		return []Model{{Value: "gpt-6-astra"}}, "account", nil
	})
	s.Get("codex")
	s.Wait()
	fail.Store(true)
	*clock = clock.Add(maxAge + time.Minute)
	s.Get("codex")
	s.Wait()
	r := s.Get("codex")
	s.Wait()
	assert.EqualValues(t, 2, calls.Load(), "not retried at once")
	assert.Equal(t, []string{"gpt-6-astra"}, values(r.Models), "the old list stays")
	assert.Equal(t, "the CLI's model list couldn't be read", r.Error)
	assert.NotContains(t, r.Error, "secret")
	*clock = clock.Add(retryAfterError + time.Minute)
	s.Get("codex")
	s.Wait()
	assert.EqualValues(t, 3, calls.Load(), "retried after retryAfterError")
}

// The Operator's Refresh fetches now, at most once per minManualGap per family.
func TestManualRefreshIsRateLimited(t *testing.T) {
	path := fakeCLI(t)
	var calls atomic.Int32
	s, clock := newTestService(t, path, func(ctx context.Context, family, p string) ([]Model, string, error) {
		calls.Add(1)
		return []Model{{Value: "x"}}, "account", nil
	})
	_, err := s.Refresh("agy")
	require.NoError(t, err)
	s.Wait()
	_, err = s.Refresh("gemini") // the same CLI (agy)
	assert.ErrorIs(t, err, ErrTooSoon)
	s.Wait()
	assert.EqualValues(t, 1, calls.Load())
	*clock = clock.Add(minManualGap + time.Second)
	_, err = s.Refresh("antigravity")
	require.NoError(t, err)
	s.Wait()
	assert.EqualValues(t, 2, calls.Load())
}

// Fetches run one at a time, whatever the number of families and callers.
func TestFetchesRunOneAtATime(t *testing.T) {
	path := fakeCLI(t)
	var running, most atomic.Int32
	var mu sync.Mutex
	s, _ := newTestService(t, path, func(ctx context.Context, family, p string) ([]Model, string, error) {
		n := running.Add(1)
		mu.Lock()
		if n > most.Load() {
			most.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return []Model{{Value: "m"}}, "account", nil
	})
	for _, typ := range []string{"claude", "codex", "agy", "pi"} {
		s.Get(typ)
	}
	s.Wait()
	assert.EqualValues(t, 1, most.Load())
}

// No list for a terminal or an unknown type, and a missing CLI is said plainly, without a fetch.
func TestGetWithoutAList(t *testing.T) {
	s := NewService(func(string) (string, error) { return "", errors.New("not found") })
	s.fetch = func(context.Context, string, string) ([]Model, string, error) {
		t.Fatal("no fetch")
		return nil, "", nil
	}
	assert.NotEmpty(t, s.Get("terminal").Error)
	assert.Equal(t, []Model{}, s.Get("terminal").Models, "always a list, never null")
	r := s.Get("claude")
	assert.NotEmpty(t, r.Error)
	assert.Equal(t, "builtin", r.Source, "a missing CLI still gets the curated list")
	assert.NotEmpty(t, r.Models)
}
