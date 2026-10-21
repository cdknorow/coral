package updatecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4133: the server's update check must not make one GitHub request per call.

func feed(t *testing.T, status int, body string, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCacheTwentyCallsMakeOneRequest(t *testing.T) {
	srv, hits := feed(t, 200, tag("v9.9.9"), 0)
	c := NewCache()
	for i := 0; i < 20; i++ {
		v, err := c.Latest(context.Background(), srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "9.9.9", v)
	}
	assert.EqualValues(t, 1, hits.Load(), "20 calls, one request to the feed")
}

func TestCacheConcurrentCallsShareOneRequest(t *testing.T) {
	srv, hits := feed(t, 200, tag("v9.9.9"), 300*time.Millisecond)
	c := NewCache()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Latest(context.Background(), srv.URL)
			assert.NoError(t, err)
			assert.Equal(t, "9.9.9", v)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, hits.Load(), "20 callers at once share the request in flight")
}

func TestCacheAGoodAnswerExpiresAfterAnHour(t *testing.T) {
	srv, hits := feed(t, 200, tag("v9.9.9"), 0)
	c := NewCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	_, _ = c.Latest(context.Background(), srv.URL)
	now = now.Add(CacheOK - time.Second)
	_, _ = c.Latest(context.Background(), srv.URL)
	assert.EqualValues(t, 1, hits.Load(), "still fresh just under the hour")
	now = now.Add(2 * time.Second)
	_, _ = c.Latest(context.Background(), srv.URL)
	assert.EqualValues(t, 2, hits.Load(), "asked again after the hour")
}

func TestCacheAFailureIsKeptShortAndNotRetriedPerCall(t *testing.T) {
	srv, hits := feed(t, 403, `{"message":"API rate limit exceeded"}`, 0)
	c := NewCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		_, err := c.Latest(context.Background(), srv.URL)
		require.Error(t, err)
	}
	assert.EqualValues(t, 1, hits.Load(), "a failing feed is asked once, not once per call")
	now = now.Add(CacheFail + time.Second)
	_, _ = c.Latest(context.Background(), srv.URL)
	assert.EqualValues(t, 2, hits.Load(), "and again after the short failure window")
}

func TestCacheRefreshIsThrottled(t *testing.T) {
	srv, hits := feed(t, 200, tag("v9.9.9"), 0)
	c := NewCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	_, _ = c.Latest(context.Background(), srv.URL)
	for i := 0; i < 10; i++ {
		_, _ = c.Refresh(context.Background(), srv.URL)
	}
	assert.EqualValues(t, 1, hits.Load(), "a refresh within a minute gets the cached answer")
	now = now.Add(RefreshAfter + time.Second)
	_, _ = c.Refresh(context.Background(), srv.URL)
	_, _ = c.Refresh(context.Background(), srv.URL)
	assert.EqualValues(t, 2, hits.Load(), "a refresh after a minute asks again, once")
}

func TestCacheACallerThatGivesUpDoesNotCancelTheRequest(t *testing.T) {
	srv, hits := feed(t, 200, tag("v9.9.9"), 300*time.Millisecond)
	c := NewCache()
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Latest(short, srv.URL)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	v, err := c.Latest(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "9.9.9", v)
	assert.EqualValues(t, 1, hits.Load(), "the second caller got the first caller's request")
}

func TestCacheFeedsAreSeparate(t *testing.T) {
	a, ha := feed(t, 200, tag("v1.0.0"), 0)
	b, hb := feed(t, 200, tag("v2.0.0"), 0)
	c := NewCache()
	va, _ := c.Latest(context.Background(), a.URL)
	vb, _ := c.Latest(context.Background(), b.URL)
	assert.Equal(t, "1.0.0", va)
	assert.Equal(t, "2.0.0", vb)
	assert.EqualValues(t, 1, ha.Load())
	assert.EqualValues(t, 1, hb.Load())
}
