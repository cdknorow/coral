package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/uploads"
)

// #3364: expired uploads go; the window is the setting; a lowered cap trims the folder.
func TestUploadsRetention(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	coralDir := t.TempDir()
	dir := filepath.Join(coralDir, "uploads")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	now := time.Now()
	put := func(name string, size int, age time.Duration) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, make([]byte, size), 0o644))
		require.NoError(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
		return p
	}
	old := put("old.png", 1, 31*24*time.Hour)
	week := put("week.png", 1, 7*24*time.Hour)
	j := NewUploadsRetention(db, coralDir)
	j.now = func() time.Time { return now }

	assert.Equal(t, 1, j.RunOnce(ctx), "30 days by default")
	assert.NoFileExists(t, old)
	assert.FileExists(t, week)

	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(ctx, uploads.RetentionSetting, "3"))
	assert.Equal(t, 1, j.RunOnce(ctx))
	assert.NoFileExists(t, week)

	// A lowered cap (here the 50 MB minimum) trims the oldest, keeping the last hour.
	big1 := put("big1.png", 30<<20, 2*time.Hour)
	big2 := put("big2.png", 30<<20, 10*time.Minute)
	require.NoError(t, ss.SetSetting(ctx, uploads.MaxMBSetting, "50"))
	j.RunOnce(ctx)
	assert.NoFileExists(t, big1)
	assert.FileExists(t, big2)
}
