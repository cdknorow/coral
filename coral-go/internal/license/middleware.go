package license

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/tracking"
)

// Middleware returns an HTTP middleware that tracks license state.
// All requests pass through regardless of license status — no routes are gated.
func Middleware(m *Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
}

// Routes registers the license API endpoints on a chi-compatible router.
// Call this with r.Post("/api/license/activate", ...) etc.
type Routes struct {
	mgr           *Manager
	webhookSecret string // Lemon Squeezy webhook signing secret (optional)
}

func NewRoutes(mgr *Manager) *Routes {
	return &Routes{mgr: mgr}
}

// SetWebhookSecret sets the secret used to verify Lemon Squeezy webhook signatures.
func (lr *Routes) SetWebhookSecret(secret string) {
	lr.webhookSecret = secret
}

// Activate handles POST /api/license/activate
func (lr *Routes) Activate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	var body struct {
		LicenseKey string `json:"license_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.LicenseKey == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "license_key is required"})
		return
	}

	body.LicenseKey = strings.TrimSpace(body.LicenseKey)

	if err := lr.mgr.Activate(body.LicenseKey); err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	info := lr.mgr.GetInfo()

	// Funnel: successful supporter-license activation. Only the non-identifying
	// product/variant name is sent — never the key, name, or email.
	props := map[string]string{}
	if info != nil {
		if info.ProductName != "" {
			props["product_name"] = info.ProductName
		}
		if info.VariantName != "" {
			props["variant_name"] = info.VariantName
		}
	}
	tracking.TrackEvent("license_activated", props)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":          true,
		"customer_name":  info.CustomerName,
		"customer_email": info.CustomerEmail,
		"activated_at":   info.ActivatedAt,
	})
}

// Status handles GET /api/license/status
func (lr *Routes) Status(w http.ResponseWriter, r *http.Request) {
	info := lr.mgr.GetInfo()
	if info == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"valid": false, "activated": false})
		return
	}

	// Calculate days until next revalidation
	var daysUntilRevalidation int
	if lastValidated, err := parseTime(info.LastValidated); err == nil {
		nextRevalidation := lastValidated.Add(7 * 24 * time.Hour)
		daysUntilRevalidation = int(time.Until(nextRevalidation).Hours() / 24)
		if daysUntilRevalidation < 0 {
			daysUntilRevalidation = 0
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":                    info.Valid,
		"activated":                true,
		"customer_name":            info.CustomerName,
		"customer_email":           info.CustomerEmail,
		"product_name":             info.ProductName,
		"activated_at":             info.ActivatedAt,
		"last_validated":           info.LastValidated,
		"machine_id":              machineFingerprint(),
		"days_until_revalidation": daysUntilRevalidation,
		"trial_ends_at":           info.TrialEndsAt,
	})
}

// Deactivate handles POST /api/license/deactivate
func (lr *Routes) Deactivate(w http.ResponseWriter, r *http.Request) {
	if err := lr.mgr.Deactivate(); err != nil {
		log.Printf("[license] deactivation failed: %v", err)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "deactivation failed — check your network connection and try again",
		})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Webhook handles POST /api/license/webhook from Lemon Squeezy.
// Verifies the signature and processes license lifecycle events.
func (lr *Routes) Webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64KB — LS payloads are small

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Verify webhook signature — reject if no secret is configured
	if lr.webhookSecret == "" {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	signature := r.Header.Get("X-Signature")
	if !verifyWebhookSignature(body, signature, lr.webhookSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event struct {
		Meta struct {
			EventName string `json:"event_name"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	switch event.Meta.EventName {
	case "license_key.revoked", "subscription_expired":
		lr.mgr.Revoke()
	case "subscription_cancelled":
		// Cancelled = user opted out, but may still have access until period ends.
		// Revoke immediately for simplicity.
		lr.mgr.Revoke()
	case "subscription_created", "subscription_updated":
		// Trial started or status changed (on_trial → active). Re-validate to refresh cache.
		lr.mgr.Revalidate()
	}

	w.WriteHeader(http.StatusOK)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// verifyWebhookSignature verifies the Lemon Squeezy webhook HMAC-SHA256 signature.
func verifyWebhookSignature(payload []byte, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
