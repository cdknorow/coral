package background

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
)

// #3627 F3: a delivery that fails leaves no part of the webhook's path or query (usually the
// secret) in its stored error.
func TestFailedDeliveryNeverStoresTheURL(t *testing.T) {
	httputil.SetWebhookAllowPrivate(func() bool { return true })
	t.Cleanup(func() { httputil.SetWebhookAllowPrivate(nil) })
	// A port nothing listens on: the client fails, with the URL in Go's error.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()

	db := setupTestDB(t)
	ws := store.NewWebhookStore(db)
	ctx := context.Background()
	url := "http://" + addr + "/services/T0001/B0002/XyZsecretPATH?token=QsecretQUERY"
	cfg, err := ws.CreateWebhookConfig(ctx, "hook", "generic", url, nil)
	require.NoError(t, err)
	del, err := ws.CreateWebhookDelivery(ctx, cfg.ID, "claude-x", "needs_input", "s", nil)
	require.NoError(t, err)

	d := NewWebhookDispatcher(ws, time.Hour)
	assert.False(t, d.deliverNow(ctx, *del))
	got, err := ws.ListWebhookDeliveries(ctx, cfg.ID, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].ErrorMsg)
	msg := *got[0].ErrorMsg
	assert.NotEmpty(t, msg)
	for _, part := range []string{"XyZsecretPATH", "QsecretQUERY", "T0001", "services", "token="} {
		assert.NotContains(t, msg, part, "stored error: %q", msg)
	}
	assert.True(t, strings.HasPrefix(msg, "post: "), "the operation and the cause: %q", msg)
}
