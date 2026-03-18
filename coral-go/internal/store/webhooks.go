package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// WebhookConfig represents a webhook subscription.
type WebhookConfig struct {
	ID                   int64   `db:"id" json:"id"`
	Name                 string  `db:"name" json:"name"`
	Platform             string  `db:"platform" json:"platform"`
	URL                  string  `db:"url" json:"url"`
	Enabled              int     `db:"enabled" json:"enabled"`
	EventFilter          string  `db:"event_filter" json:"event_filter"`
	IdleThresholdSeconds int     `db:"idle_threshold_seconds" json:"idle_threshold_seconds"`
	AgentFilter          *string `db:"agent_filter" json:"agent_filter,omitempty"`
	LowConfidenceOnly    int     `db:"low_confidence_only" json:"low_confidence_only"`
	ConsecutiveFailures  int     `db:"consecutive_failures" json:"consecutive_failures"`
	CreatedAt            string  `db:"created_at" json:"created_at"`
	UpdatedAt            string  `db:"updated_at" json:"updated_at"`
}

// WebhookDelivery represents a webhook delivery attempt.
type WebhookDelivery struct {
	ID           int64   `db:"id" json:"id"`
	WebhookID    int64   `db:"webhook_id" json:"webhook_id"`
	AgentName    string  `db:"agent_name" json:"agent_name"`
	SessionID    *string `db:"session_id" json:"session_id,omitempty"`
	EventType    string  `db:"event_type" json:"event_type"`
	EventSummary string  `db:"event_summary" json:"event_summary"`
	Status       string  `db:"status" json:"status"`
	HTTPStatus   *int    `db:"http_status" json:"http_status,omitempty"`
	ErrorMsg     *string `db:"error_msg" json:"error_msg,omitempty"`
	AttemptCount int     `db:"attempt_count" json:"attempt_count"`
	NextRetryAt  *string `db:"next_retry_at" json:"next_retry_at,omitempty"`
	DeliveredAt  *string `db:"delivered_at" json:"delivered_at,omitempty"`
	CreatedAt    string  `db:"created_at" json:"created_at"`
}

// WebhookStore provides webhook config and delivery operations.
type WebhookStore struct {
	db *DB
}

// NewWebhookStore creates a new WebhookStore.
func NewWebhookStore(db *DB) *WebhookStore {
	return &WebhookStore{db: db}
}

// ── Webhook Configs ────────────────────────────────────────────────────

// ListWebhookConfigs returns all webhook configs.
func (s *WebhookStore) ListWebhookConfigs(ctx context.Context, enabledOnly bool) ([]WebhookConfig, error) {
	var configs []WebhookConfig
	query := "SELECT * FROM webhook_configs ORDER BY created_at"
	if enabledOnly {
		query = "SELECT * FROM webhook_configs WHERE enabled = 1 ORDER BY created_at"
	}
	err := s.db.SelectContext(ctx, &configs, query)
	return configs, err
}

// GetWebhookConfig returns a webhook config by ID.
func (s *WebhookStore) GetWebhookConfig(ctx context.Context, webhookID int64) (*WebhookConfig, error) {
	var cfg WebhookConfig
	err := s.db.GetContext(ctx, &cfg, "SELECT * FROM webhook_configs WHERE id = ?", webhookID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &cfg, err
}

// CreateWebhookConfig creates a new webhook config.
func (s *WebhookStore) CreateWebhookConfig(ctx context.Context, name, platform, url string, agentFilter *string) (*WebhookConfig, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO webhook_configs
		 (name, platform, url, enabled, event_filter, idle_threshold_seconds,
		  agent_filter, low_confidence_only, consecutive_failures, created_at, updated_at)
		 VALUES (?, ?, ?, 1, '*', 0, ?, 0, 0, ?, ?)`,
		name, platform, url, agentFilter, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return s.GetWebhookConfig(ctx, id)
}

// UpdateWebhookConfig updates allowed fields on a webhook config.
func (s *WebhookStore) UpdateWebhookConfig(ctx context.Context, webhookID int64, fields map[string]interface{}) error {
	allowed := map[string]bool{
		"name": true, "platform": true, "url": true, "enabled": true,
		"event_filter": true, "idle_threshold_seconds": true,
		"agent_filter": true, "low_confidence_only": true, "consecutive_failures": true,
	}
	now := nowUTC()
	sets := []string{"updated_at = ?"}
	args := []interface{}{now}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = ?", k))
		args = append(args, v)
	}
	args = append(args, webhookID)
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE webhook_configs SET %s WHERE id = ?", strings.Join(sets, ", ")),
		args...)
	return err
}

// DeleteWebhookConfig deletes a webhook config and its deliveries.
func (s *WebhookStore) DeleteWebhookConfig(ctx context.Context, webhookID int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.ExecContext(ctx, "DELETE FROM webhook_deliveries WHERE webhook_id = ?", webhookID)
	tx.ExecContext(ctx, "DELETE FROM webhook_configs WHERE id = ?", webhookID)
	return tx.Commit()
}

// ── Webhook Deliveries ─────────────────────────────────────────────────

// CreateWebhookDelivery creates a new pending delivery and prunes old ones.
func (s *WebhookStore) CreateWebhookDelivery(ctx context.Context, webhookID int64, agentName, eventType, eventSummary string, sessionID *string) (*WebhookDelivery, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries
		 (webhook_id, agent_name, session_id, event_type, event_summary, status, attempt_count, created_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', 0, ?)`,
		webhookID, agentName, sessionID, eventType, eventSummary, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()

	// Prune to 200 deliveries per webhook (excluding pending)
	s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE webhook_id = ?
		 AND status != 'pending' AND id NOT IN
		 (SELECT id FROM webhook_deliveries WHERE webhook_id = ?
		  AND status != 'pending' ORDER BY id DESC LIMIT 200)`,
		webhookID, webhookID)

	return &WebhookDelivery{
		ID: id, WebhookID: webhookID, AgentName: agentName,
		SessionID: sessionID, EventType: eventType, EventSummary: eventSummary,
		Status: "pending", AttemptCount: 0, CreatedAt: now,
	}, nil
}

// MarkWebhookDelivery updates the status of a delivery.
func (s *WebhookStore) MarkWebhookDelivery(ctx context.Context, deliveryID int64, status string, httpStatus *int, errorMsg *string, attemptCount *int, nextRetryAt *string) error {
	now := nowUTC()
	var deliveredAt *string
	if status == "delivered" {
		deliveredAt = &now
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_deliveries
		 SET status = ?, http_status = ?, error_msg = ?,
		     attempt_count = COALESCE(?, attempt_count),
		     next_retry_at = ?, delivered_at = ?
		 WHERE id = ?`,
		status, httpStatus, errorMsg, attemptCount, nextRetryAt, deliveredAt, deliveryID)
	return err
}

// GetPendingWebhookDeliveries returns deliveries ready for sending.
func (s *WebhookStore) GetPendingWebhookDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var deliveries []WebhookDelivery
	err := s.db.SelectContext(ctx, &deliveries,
		`SELECT * FROM webhook_deliveries
		 WHERE status = 'pending' AND (next_retry_at IS NULL OR next_retry_at <= ?)
		 ORDER BY created_at LIMIT ?`,
		now, limit)
	return deliveries, err
}

// ListWebhookDeliveries returns recent deliveries for a webhook.
func (s *WebhookStore) ListWebhookDeliveries(ctx context.Context, webhookID int64, limit int) ([]WebhookDelivery, error) {
	var deliveries []WebhookDelivery
	err := s.db.SelectContext(ctx, &deliveries,
		`SELECT * FROM webhook_deliveries WHERE webhook_id = ?
		 ORDER BY created_at DESC LIMIT ?`,
		webhookID, limit)
	return deliveries, err
}

// IncrementConsecutiveFailures increments the failure counter and returns the new value.
func (s *WebhookStore) IncrementConsecutiveFailures(ctx context.Context, webhookID int64) (int, error) {
	_, err := s.db.ExecContext(ctx,
		"UPDATE webhook_configs SET consecutive_failures = consecutive_failures + 1 WHERE id = ?",
		webhookID)
	if err != nil {
		return 0, err
	}
	cfg, err := s.GetWebhookConfig(ctx, webhookID)
	if err != nil || cfg == nil {
		return 0, err
	}
	return cfg.ConsecutiveFailures, nil
}

// ResetConsecutiveFailures resets the failure counter to zero.
func (s *WebhookStore) ResetConsecutiveFailures(ctx context.Context, webhookID int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE webhook_configs SET consecutive_failures = 0 WHERE id = ?", webhookID)
	return err
}

// AutoDisableWebhook disables a webhook due to too many failures.
func (s *WebhookStore) AutoDisableWebhook(ctx context.Context, webhookID int64) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		"UPDATE webhook_configs SET enabled = 0, updated_at = ? WHERE id = ?",
		now, webhookID)
	return err
}
