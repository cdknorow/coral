package background

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

var (
	retryDelays            = []int{30, 120, 600}   // 30s, 2m, 10m
	circuitBreakerThreshold = 10
)

// WebhookDispatcher flushes pending webhook deliveries with retry and circuit breaker.
type WebhookDispatcher struct {
	store    *store.WebhookStore
	client   *http.Client
	interval time.Duration
	logger   *slog.Logger
}

// NewWebhookDispatcher creates a new WebhookDispatcher.
func NewWebhookDispatcher(webhookStore *store.WebhookStore, interval time.Duration) *WebhookDispatcher {
	return &WebhookDispatcher{
		store:    webhookStore,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: interval,
		logger:   slog.Default().With("service", "webhook_dispatcher"),
	}
}

// Run starts the dispatch loop.
func (d *WebhookDispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := d.RunOnce(ctx); err != nil {
				d.logger.Error("dispatch error", "error", err)
			}
		}
	}
}

// RunOnce flushes pending deliveries.
func (d *WebhookDispatcher) RunOnce(ctx context.Context) error {
	pending, err := d.store.GetPendingWebhookDeliveries(ctx, 50)
	if err != nil {
		return err
	}
	for _, delivery := range pending {
		d.deliverNow(ctx, delivery)
	}
	return nil
}

func (d *WebhookDispatcher) deliverNow(ctx context.Context, delivery store.WebhookDelivery) bool {
	cfg, err := d.store.GetWebhookConfig(ctx, delivery.WebhookID)
	if err != nil || cfg == nil || cfg.Enabled == 0 {
		errMsg := "Webhook disabled or deleted"
		d.store.MarkWebhookDelivery(ctx, delivery.ID, "failed", nil, &errMsg, nil, nil)
		return false
	}

	payload := buildPayload(cfg.Platform, delivery)
	attempt := delivery.AttemptCount + 1

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL, bytes.NewReader(body))
	if err != nil {
		errMsg := err.Error()
		d.scheduleRetryOrFail(ctx, cfg, delivery, attempt, nil, errMsg)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		d.scheduleRetryOrFail(ctx, cfg, delivery, attempt, nil, errMsg)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		status := resp.StatusCode
		d.store.MarkWebhookDelivery(ctx, delivery.ID, "delivered", &status, nil, &attempt, nil)
		d.store.ResetConsecutiveFailures(ctx, cfg.ID)
		return true
	}

	status := resp.StatusCode
	errMsg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	d.scheduleRetryOrFail(ctx, cfg, delivery, attempt, &status, errMsg)
	return false
}

func (d *WebhookDispatcher) scheduleRetryOrFail(ctx context.Context, cfg *store.WebhookConfig, delivery store.WebhookDelivery, attempt int, httpStatus *int, errMsg string) {
	failureCount, _ := d.store.IncrementConsecutiveFailures(ctx, cfg.ID)
	if failureCount >= circuitBreakerThreshold {
		d.store.AutoDisableWebhook(ctx, cfg.ID)
		d.logger.Warn("webhook auto-disabled", "id", cfg.ID, "name", cfg.Name, "failures", failureCount)
	}

	if attempt > len(retryDelays) {
		d.store.MarkWebhookDelivery(ctx, delivery.ID, "failed", httpStatus, &errMsg, &attempt, nil)
		return
	}
	delay := retryDelays[attempt-1]
	nextRetry := time.Now().UTC().Add(time.Duration(delay) * time.Second).Format(time.RFC3339)
	d.store.MarkWebhookDelivery(ctx, delivery.ID, "pending", httpStatus, &errMsg, &attempt, &nextRetry)
}

// ── Payload builders ─────────────────────────────────────────────────

func buildPayload(platform string, delivery store.WebhookDelivery) map[string]interface{} {
	switch platform {
	case "slack":
		return slackPayload(delivery)
	case "discord":
		return discordPayload(delivery)
	default:
		return genericPayload(delivery)
	}
}

func slackPayload(d store.WebhookDelivery) map[string]interface{} {
	emoji := map[string]string{
		"needs_input": ":raising_hand:",
		"status":      ":large_blue_circle:",
	}
	e, ok := emoji[d.EventType]
	if !ok {
		e = ":bell:"
	}
	return map[string]interface{}{
		"blocks": []map[string]interface{}{
			{
				"type": "section",
				"text": map[string]interface{}{
					"type": "mrkdwn",
					"text": fmt.Sprintf("%s *Coral — %s*\n*Agent:* `%s`\n*Message:* %s",
						e, d.EventType, d.AgentName, d.EventSummary),
				},
			},
		},
	}
}

func discordPayload(d store.WebhookDelivery) map[string]interface{} {
	color := 0x58A6FF
	if d.EventType == "needs_input" {
		color = 0xD29922
	}
	return map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       fmt.Sprintf("Coral — %s", d.EventType),
				"description": d.EventSummary,
				"color":       color,
				"fields": []map[string]interface{}{
					{"name": "Agent", "value": fmt.Sprintf("`%s`", d.AgentName), "inline": true},
				},
				"footer": map[string]interface{}{"text": "Coral"},
			},
		},
	}
}

func genericPayload(d store.WebhookDelivery) map[string]interface{} {
	return map[string]interface{}{
		"agent_name": d.AgentName,
		"session_id": d.SessionID,
		"event_type": d.EventType,
		"summary":    d.EventSummary,
		"timestamp":  d.CreatedAt,
		"source":     "coral",
	}
}
