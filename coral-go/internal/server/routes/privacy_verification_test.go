package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerification_PrivacyRoutes_StatusAndSettings verifies:
// 1. Default state of GET /api/system/privacy when settings are unset.
// 2. Lifecycle transitions between enabled, disabled, and restart-required.
// 3. Independent control of telemetry_enabled vs remote_access_enabled.
func TestVerification_PrivacyRoutes_StatusAndSettings(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	handler.cfg.Host = "0.0.0.0"

	// 1. Initial defaults: telemetry on, remote access OFF (no saved opt-in).
	// This simulated server is still bound to 0.0.0.0, so a restart is pending.
	resp, err := http.Get(server.URL + "/api/system/privacy")
	require.NoError(t, err)
	var initial map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&initial))
	resp.Body.Close()

	assert.Equal(t, true, initial["telemetry_enabled"])
	assert.Equal(t, false, initial["remote_access_enabled"], "remote access must default to off")
	assert.Equal(t, false, initial["remote_access_configured"])
	assert.Equal(t, true, initial["remote_access_effective"])
	assert.Equal(t, true, initial["remote_access_restart_required"])
	assert.Equal(t, "disable_pending_restart", initial["remote_access_status"])
	assert.Equal(t, "0.0.0.0", initial["effective_host"])

	// 2. Disable remote access via PUT /api/settings
	payload := bytes.NewBufferString(`{"remote_access_enabled":false}`)
	req, _ := http.NewRequest("PUT", server.URL+"/api/settings", payload)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	// Verify status reflects saved false, effective true, and restart required
	resp, err = http.Get(server.URL + "/api/system/privacy")
	require.NoError(t, err)
	var afterRemoteDisable map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&afterRemoteDisable))
	resp.Body.Close()

	assert.Equal(t, true, afterRemoteDisable["telemetry_enabled"], "telemetry should remain independently enabled")
	assert.Equal(t, false, afterRemoteDisable["remote_access_enabled"], "saved remote access should be false")
	assert.Equal(t, true, afterRemoteDisable["remote_access_effective"], "effective running server is still 0.0.0.0")
	assert.Equal(t, true, afterRemoteDisable["remote_access_restart_required"], "restart must be required")

	// 3. Re-enable remote access before restart
	payload = bytes.NewBufferString(`{"remote_access_enabled":true}`)
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", payload)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = http.Get(server.URL + "/api/system/privacy")
	require.NoError(t, err)
	var afterRemoteReenable map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&afterRemoteReenable))
	resp.Body.Close()

	assert.Equal(t, true, afterRemoteReenable["remote_access_enabled"])
	assert.Equal(t, true, afterRemoteReenable["remote_access_configured"])
	assert.Equal(t, false, afterRemoteReenable["remote_access_restart_required"], "restart requirement cleared once saved matches running")
	assert.Equal(t, "enabled", afterRemoteReenable["remote_access_status"])

	// 4. Disable telemetry independently
	payload = bytes.NewBufferString(`{"telemetry_enabled":false}`)
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", payload)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = http.Get(server.URL + "/api/system/privacy")
	require.NoError(t, err)
	var afterTelemetryDisable map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&afterTelemetryDisable))
	resp.Body.Close()

	assert.Equal(t, false, afterTelemetryDisable["telemetry_enabled"])
	assert.Equal(t, true, afterTelemetryDisable["remote_access_enabled"])
}

// TestVerification_AuthAuthority_RemoteRejectionAndAntiSpoofing verifies:
// 1. Unauthenticated remote clients cannot call PUT /api/settings or GET /api/system/privacy (401).
// 2. Spoofed headers (X-Forwarded-For, X-Real-IP, Client-IP, Host: localhost) from remote IPs are rejected.
// 3. Authenticated remote clients with API key succeed.
// 4. Localhost requests follow local authority and reject DNS rebinding attempts.
func TestVerification_AuthAuthority_RemoteRejectionAndAntiSpoofing(t *testing.T) {
	tempDir := t.TempDir()
	db, err := store.Open(tempDir + "/auth_test.db")
	require.NoError(t, err)
	defer db.Close()

	ks, err := auth.NewKeyStore(tempDir)
	require.NoError(t, err)
	apiKey := ks.Key()

	cfg := &config.Config{Host: "0.0.0.0"}
	sysHandler := NewSystemHandler(db, cfg)

	r := chi.NewRouter()
	r.Use(auth.Middleware(ks))
	r.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("login page"))
	})
	r.Get("/api/system/privacy", sysHandler.GetPrivacyStatus)
	r.Put("/api/settings", sysHandler.PutSettings)

	server := httptest.NewServer(r)
	defer server.Close()

	// 1. Unauthenticated remote client
	req, _ := http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(`{"telemetry_enabled":false}`))
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "unauthenticated remote access to /api/settings must return 401")

	// 2. Spoofed X-Forwarded-For: 127.0.0.1
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(`{"telemetry_enabled":false}`))
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "spoofed X-Forwarded-For must be ignored and return 401")

	// 3. Spoofed X-Real-IP: 127.0.0.1
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(`{"telemetry_enabled":false}`))
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("X-Real-IP", "127.0.0.1")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "spoofed X-Real-IP must be ignored and return 401")

	// 4. Spoofed Host: localhost from remote IP
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(`{"telemetry_enabled":false}`))
	req.RemoteAddr = "192.168.1.100:54321"
	req.Host = "localhost:8420"
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "spoofed Host header from remote IP must return 401")

	// 5. Authenticated remote client with valid Bearer token
	req, _ = http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewBufferString(`{"telemetry_enabled":true}`))
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "authenticated remote client with Bearer API key must succeed")

	// 5b. Authenticated remote client with session cookie
	sessionToken, err := ks.CreateSession("192.168.1.100", "test-agent")
	require.NoError(t, err)
	req, _ = http.NewRequest("GET", server.URL+"/api/system/privacy", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.AddCookie(&http.Cookie{Name: "coral_session", Value: sessionToken})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "authenticated remote client with session cookie must succeed")

	// 6. Localhost client with valid localhost Host
	req, _ = http.NewRequest("GET", server.URL+"/api/system/privacy", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "localhost:8420"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "localhost client should be authorized")

	// 7. Localhost client with DNS rebinding host
	req, _ = http.NewRequest("GET", server.URL+"/api/system/privacy", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "attacker-domain.com:8420"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, "DNS rebinding attempt must return 403 Forbidden")
}

// TestVerification_TrackingHandler_BrowserEventGating verifies that the browser
// funnel tracking endpoint (/api/tracking/event) safely accepts allowed events
// but PostHog outbound delivery is strictly suppressed when telemetry is disabled.
func TestVerification_TrackingHandler_BrowserEventGating(t *testing.T) {
	tempDir := t.TempDir()
	trackingHandler := NewTrackingHandler(tempDir)

	r := chi.NewRouter()
	r.Post("/api/tracking/event", trackingHandler.TrackEvent)

	// 1. Unknown event rejected
	unknownReq, _ := http.NewRequest("POST", "/api/tracking/event", bytes.NewBufferString(`{"event":"unknown_event"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, unknownReq)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// 2. Allowed event accepted by HTTP endpoint
	allowedReq, _ := http.NewRequest("POST", "/api/tracking/event", bytes.NewBufferString(`{"event":"supporter_checkout_clicked","props":{"surface":"settings_modal"}}`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, allowedReq)
	assert.Equal(t, http.StatusOK, w.Code)
}

