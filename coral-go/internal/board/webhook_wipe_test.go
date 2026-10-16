package board

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3646: a subscription's webhook URL left in messageboard.db by an older Coral is found in no
// byte of the file or its WAL after the wipe; a second run changes nothing.
func TestWipeWebhookURLsLeavesNoTraceInTheFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messageboard.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	_, err = s.Subscribe(ctx, "team", "Dev", "Dev", "s1", nil, "")
	require.NoError(t, err)
	const marker = "WHMARK-BOARD-6a2f81c3"
	url := "https://hooks.example.com/" + strings.Repeat("p", 12000) + "/" + marker
	_, err = s.db.ExecContext(ctx, "UPDATE board_subscribers SET webhook_url = ? WHERE subscriber_id = 'Dev'", url)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	require.NoError(t, err)

	n, err := s.WipeWebhookURLs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		assert.False(t, bytes.Contains(b, []byte(marker)), "%s still holds the URL", filepath.Base(f))
	}
	n, err = s.WipeWebhookURLs(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}
