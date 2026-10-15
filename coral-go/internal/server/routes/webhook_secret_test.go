package routes

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3627 F3: the webhook list shows each URL masked; only the one-webhook read the edit form
// makes returns it whole; delivery history never shows a URL, even from a row stored before.
func TestWebhookURLsAreMaskedOutsideAnEdit(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/wh.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	wh := NewWebhooksHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Get("/api/webhooks", wh.ListWebhooks)
	r.Get("/api/webhooks/{webhookID}", wh.GetWebhook)
	r.Get("/api/webhooks/{webhookID}/deliveries", wh.ListDeliveries)

	ctx := context.Background()
	ws := store.NewWebhookStore(db)
	url := "https://hooks.slack.com/services/T0001/B0002/XyZsecretPATH"
	cfg, err := ws.CreateWebhookConfig(ctx, "team", "slack", url, nil)
	require.NoError(t, err)
	del, err := ws.CreateWebhookDelivery(ctx, cfg.ID, "claude-x", "needs_input", "s", nil)
	require.NoError(t, err)
	old := `Post "` + url + `": dial tcp: connection refused`
	ws.MarkWebhookDelivery(ctx, del.ID, "failed", nil, &old, nil, nil)
	id := strconv.FormatInt(cfg.ID, 10)

	w := send(t, r, "GET", "/api/webhooks", nil)
	require.Equal(t, 200, w.Code)
	assert.NotContains(t, w.Body.String(), "XyZsecretPATH", "the list masks the URL")
	assert.Contains(t, w.Body.String(), "https://hooks.slack.com/…PATH")

	w = send(t, r, "GET", "/api/webhooks/"+id, nil)
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "XyZsecretPATH", "the edit read has it whole")
	assert.Equal(t, 404, send(t, r, "GET", "/api/webhooks/99999", nil).Code)

	w = send(t, r, "GET", "/api/webhooks/"+id+"/deliveries", nil)
	require.Equal(t, 200, w.Code)
	assert.NotContains(t, w.Body.String(), "XyZsecretPATH", "an old stored error is scrubbed")
	assert.Contains(t, w.Body.String(), "connection refused")
}
