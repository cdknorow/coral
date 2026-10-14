package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
)

// WebhooksHandler handles webhook configuration and delivery endpoints.
type WebhooksHandler struct {
	ws  *store.WebhookStore
	cfg *config.Config
}

func NewWebhooksHandler(db *store.DB, cfg *config.Config) *WebhooksHandler {
	return &WebhooksHandler{
		ws:  store.NewWebhookStore(db),
		cfg: cfg,
	}
}

// ListWebhooks returns all webhook configurations.
// GET /api/webhooks
func (h *WebhooksHandler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	configs, err := h.ws.ListWebhookConfigs(r.Context(), false)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(configs))
}

// CreateWebhook creates a new webhook.
// POST /api/webhooks
func (h *WebhooksHandler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Platform    string  `json:"platform"`
		URL         string  `json:"url"`
		AgentFilter *string `json:"agent_filter"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Name == "" || body.URL == "" {
		errBadRequest(w, "name and url are required")
		return
	}
	if body.Platform == "" {
		body.Platform = "generic"
	}
	if err := httputil.ValidateWebhookURL(r.Context(), body.URL); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	wh, err := h.ws.CreateWebhookConfig(r.Context(), body.Name, body.Platform, body.URL, body.AgentFilter)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wh)
}

// UpdateWebhook updates a webhook configuration.
// PATCH /api/webhooks/{webhookID}
func (h *WebhooksHandler) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	whID, _ := strconv.ParseInt(chi.URLParam(r, "webhookID"), 10, 64)
	var fields map[string]interface{}
	if err := decodeJSON(r, &fields); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if v, ok := fields["url"]; ok {
		u, _ := v.(string)
		if err := httputil.ValidateWebhookURL(r.Context(), u); err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}
	if err := h.ws.UpdateWebhookConfig(r.Context(), whID, fields); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeleteWebhook deletes a webhook and its delivery history.
// DELETE /api/webhooks/{webhookID}
func (h *WebhooksHandler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	whID, _ := strconv.ParseInt(chi.URLParam(r, "webhookID"), 10, 64)
	if err := h.ws.DeleteWebhookConfig(r.Context(), whID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// TestWebhook sends a test notification immediately via direct delivery.
// POST /api/webhooks/{webhookID}/test
func (h *WebhooksHandler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	whID, _ := strconv.ParseInt(chi.URLParam(r, "webhookID"), 10, 64)
	cfg, err := h.ws.GetWebhookConfig(r.Context(), whID)
	if err != nil || cfg == nil {
		errNotFound(w, "Webhook not found")
		return
	}
	// SSRF protection: validate webhook URL doesn't target internal networks
	if _, err := httputil.ResolveAndValidateURL(cfg.URL); err != nil {
		errBadRequest(w, fmt.Sprintf("webhook URL blocked: %v", err))
		return
	}
	delivery, err := h.ws.CreateWebhookDelivery(
		r.Context(), whID, "coral-test", "needs_input",
		"Test notification from Coral dashboard", nil,
	)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Deliver immediately inline (same as Python's deliver_now)
	go h.deliverTestWebhook(cfg, delivery)
	// Return the delivery record
	writeJSON(w, http.StatusOK, delivery)
}

func (h *WebhooksHandler) deliverTestWebhook(cfg *store.WebhookConfig, delivery *store.WebhookDelivery) {
	ctx := context.Background()
	payload := map[string]interface{}{
		"agent_name": delivery.AgentName,
		"session_id": delivery.SessionID,
		"event_type": delivery.EventType,
		"summary":    delivery.EventSummary,
		"timestamp":  delivery.CreatedAt,
		"source":     "coral",
	}
	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL, bytes.NewReader(body))
	if err != nil {
		errMsg := err.Error()
		h.ws.MarkWebhookDelivery(ctx, delivery.ID, "failed", nil, &errMsg, nil, nil)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		h.ws.MarkWebhookDelivery(ctx, delivery.ID, "failed", nil, &errMsg, nil, nil)
		return
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	if status >= 200 && status < 300 {
		h.ws.MarkWebhookDelivery(ctx, delivery.ID, "delivered", &status, nil, nil, nil)
	} else {
		errMsg := fmt.Sprintf("HTTP %d", status)
		h.ws.MarkWebhookDelivery(ctx, delivery.ID, "failed", &status, &errMsg, nil, nil)
	}
}

// ListDeliveries returns delivery history for a webhook.
// GET /api/webhooks/{webhookID}/deliveries
func (h *WebhooksHandler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	whID, _ := strconv.ParseInt(chi.URLParam(r, "webhookID"), 10, 64)
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	deliveries, err := h.ws.ListWebhookDeliveries(r.Context(), whID, limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(deliveries))
}
