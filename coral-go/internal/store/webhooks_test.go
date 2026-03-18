package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookConfigCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewWebhookStore(db)
	ctx := context.Background()

	cfg, err := s.CreateWebhookConfig(ctx, "Slack Alert", "slack", "https://hooks.slack.com/xxx", nil)
	require.NoError(t, err)
	assert.Equal(t, "Slack Alert", cfg.Name)
	assert.Equal(t, 1, cfg.Enabled)
	assert.Equal(t, "*", cfg.EventFilter)

	// List
	configs, err := s.ListWebhookConfigs(ctx, false)
	require.NoError(t, err)
	assert.Len(t, configs, 1)

	// Update
	err = s.UpdateWebhookConfig(ctx, cfg.ID, map[string]interface{}{
		"event_filter": "idle,confidence",
		"enabled":      0,
	})
	require.NoError(t, err)

	updated, _ := s.GetWebhookConfig(ctx, cfg.ID)
	assert.Equal(t, "idle,confidence", updated.EventFilter)
	assert.Equal(t, 0, updated.Enabled)

	// Enabled-only should be empty
	configs, _ = s.ListWebhookConfigs(ctx, true)
	assert.Empty(t, configs)

	// Delete
	err = s.DeleteWebhookConfig(ctx, cfg.ID)
	require.NoError(t, err)
	configs, _ = s.ListWebhookConfigs(ctx, false)
	assert.Empty(t, configs)
}

func TestWebhookDeliveryCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewWebhookStore(db)
	ctx := context.Background()

	cfg, _ := s.CreateWebhookConfig(ctx, "Test", "generic", "https://example.com/hook", nil)

	// Create delivery
	delivery, err := s.CreateWebhookDelivery(ctx, cfg.ID, "agent-1", "idle", "Agent is idle", nil)
	require.NoError(t, err)
	assert.Equal(t, "pending", delivery.Status)
	assert.Equal(t, 0, delivery.AttemptCount)

	// Get pending
	pending, err := s.GetPendingWebhookDeliveries(ctx, 50)
	require.NoError(t, err)
	assert.Len(t, pending, 1)

	// Mark delivered
	httpStatus := 200
	attemptCount := 1
	err = s.MarkWebhookDelivery(ctx, delivery.ID, "delivered", &httpStatus, nil, &attemptCount, nil)
	require.NoError(t, err)

	pending, _ = s.GetPendingWebhookDeliveries(ctx, 50)
	assert.Empty(t, pending) // No more pending

	// List deliveries
	deliveries, err := s.ListWebhookDeliveries(ctx, cfg.ID, 50)
	require.NoError(t, err)
	assert.Len(t, deliveries, 1)
	assert.Equal(t, "delivered", deliveries[0].Status)
}

func TestWebhookConsecutiveFailures(t *testing.T) {
	db := openTestDB(t)
	s := NewWebhookStore(db)
	ctx := context.Background()

	cfg, _ := s.CreateWebhookConfig(ctx, "Flaky", "generic", "https://example.com", nil)

	// Increment failures
	count, err := s.IncrementConsecutiveFailures(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	count, _ = s.IncrementConsecutiveFailures(ctx, cfg.ID)
	assert.Equal(t, 2, count)

	// Reset
	err = s.ResetConsecutiveFailures(ctx, cfg.ID)
	require.NoError(t, err)

	updated, _ := s.GetWebhookConfig(ctx, cfg.ID)
	assert.Equal(t, 0, updated.ConsecutiveFailures)

	// Auto-disable
	err = s.AutoDisableWebhook(ctx, cfg.ID)
	require.NoError(t, err)

	updated, _ = s.GetWebhookConfig(ctx, cfg.ID)
	assert.Equal(t, 0, updated.Enabled)
}

func TestWebhookDeliveryPruning(t *testing.T) {
	db := openTestDB(t)
	s := NewWebhookStore(db)
	ctx := context.Background()

	cfg, _ := s.CreateWebhookConfig(ctx, "Prune Test", "generic", "https://example.com", nil)

	// Create 5 deliveries and mark them delivered
	for i := 0; i < 5; i++ {
		d, _ := s.CreateWebhookDelivery(ctx, cfg.ID, "agent-1", "test", "event", nil)
		httpStatus := 200
		attemptCount := 1
		s.MarkWebhookDelivery(ctx, d.ID, "delivered", &httpStatus, nil, &attemptCount, nil)
	}

	// Should have 5
	deliveries, _ := s.ListWebhookDeliveries(ctx, cfg.ID, 100)
	assert.Len(t, deliveries, 5)
}

func TestDeleteWebhookCascadesDeliveries(t *testing.T) {
	db := openTestDB(t)
	s := NewWebhookStore(db)
	ctx := context.Background()

	cfg, _ := s.CreateWebhookConfig(ctx, "Cascade", "generic", "https://example.com", nil)
	s.CreateWebhookDelivery(ctx, cfg.ID, "agent-1", "test", "event", nil)

	err := s.DeleteWebhookConfig(ctx, cfg.ID)
	require.NoError(t, err)

	deliveries, _ := s.ListWebhookDeliveries(ctx, cfg.ID, 50)
	assert.Empty(t, deliveries)
}
