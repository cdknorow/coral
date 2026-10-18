package routes

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The file list cache keeps at most 32 repositories (the oldest out) and does not keep a list of more than
// 200,000 files.
func TestFileListCacheIsBounded(t *testing.T) {
	c := newFileListCache(time.Hour)
	var calls atomic.Int32
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { now = now.Add(time.Second); return now }
	c.list = func(ctx context.Context, w string) ([]string, error) {
		calls.Add(1)
		if strings.HasSuffix(w, "huge") {
			return make([]string, fileListMaxFiles+1), nil
		}
		if strings.HasSuffix(w, "limit") {
			return make([]string, fileListMaxFiles), nil
		}
		return []string{"a.go"}, nil
	}
	ctx := context.Background()
	for i := 0; i < fileListMaxEntries+8; i++ {
		_, err := c.get(ctx, fmt.Sprintf("/repo/%d", i))
		require.NoError(t, err)
	}
	c.mu.Lock()
	n := len(c.entries)
	_, oldest := c.entries["/repo/0"]
	_, newest := c.entries[fmt.Sprintf("/repo/%d", fileListMaxEntries+7)]
	c.mu.Unlock()
	assert.Equal(t, fileListMaxEntries, n)
	assert.False(t, oldest, "the oldest went first")
	assert.True(t, newest)
	before := calls.Load()
	_, _ = c.get(ctx, fmt.Sprintf("/repo/%d", fileListMaxEntries+7))
	assert.Equal(t, before, calls.Load(), "a kept list is served from the cache")
	_, _ = c.get(ctx, "/repo/0")
	assert.Equal(t, before+1, calls.Load(), "an evicted one is listed again")

	// 200,000 files are kept; one more is answered but not kept.
	files, err := c.get(ctx, "/repo/limit")
	require.NoError(t, err)
	assert.Len(t, files, fileListMaxFiles)
	calls.Store(0)
	_, _ = c.get(ctx, "/repo/limit")
	assert.EqualValues(t, 0, calls.Load(), "a list at the limit is kept")
	files, err = c.get(ctx, "/repo/huge")
	require.NoError(t, err)
	assert.Len(t, files, fileListMaxFiles+1, "answered in full")
	_, _ = c.get(ctx, "/repo/huge")
	assert.EqualValues(t, 2, calls.Load(), "but listed again each time")
}
