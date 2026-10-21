package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/agent"
)

func privacyGetJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func privacyPutSettings(t *testing.T, base, body string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", base+"/api/settings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// networkInfoServer serves only NetworkInfo (the shared test router omits it).
func networkInfoServer(t *testing.T, h *SystemHandler) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/system/network-info", h.NetworkInfo)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// A default install is local-only: status must say disabled with nothing pending.
func TestRemoteAccessStatus_DefaultInstallIsLocalOnly(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	handler.cfg.Host = "127.0.0.1"

	s := privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, false, s["remote_access_enabled"])
	assert.Equal(t, false, s["remote_access_configured"])
	assert.Equal(t, false, s["remote_access_effective"])
	assert.Equal(t, false, s["remote_access_restart_required"])
	assert.Equal(t, "disabled", s["remote_access_status"])
	assert.Equal(t, "127.0.0.1", s["effective_host"])
	assert.Equal(t, false, privacyGetJSON(t, networkInfoServer(t, handler).URL+"/api/system/network-info")["remote_access_effective"])
}

// Enabling saves the opt-in but changes nothing live: restart is required.
func TestRemoteAccessStatus_EnableIsPendingUntilRestart(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	handler.cfg.Host = "127.0.0.1"

	privacyPutSettings(t, server.URL, `{"remote_access_enabled":true}`)
	s := privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, true, s["remote_access_enabled"])
	assert.Equal(t, true, s["remote_access_configured"])
	assert.Equal(t, false, s["remote_access_effective"], "saving the setting must not rebind the running server")
	assert.Equal(t, true, s["remote_access_restart_required"])
	assert.Equal(t, "enable_pending_restart", s["remote_access_status"])
	assert.Equal(t, "127.0.0.1", handler.cfg.Host, "no live rebind")

	// After a restart with the opt-in, the running bind matches.
	handler.cfg.Host = "0.0.0.0"
	s = privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, "enabled", s["remote_access_status"])
	assert.Equal(t, false, s["remote_access_restart_required"])
	assert.Equal(t, true, privacyGetJSON(t, networkInfoServer(t, handler).URL+"/api/system/network-info")["remote_access_effective"])
}

// Anything but the literal "true" is not an opt-in.
func TestRemoteAccessStatus_OnlyExplicitTrueEnables(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	handler.cfg.Host = "127.0.0.1"
	privacyPutSettings(t, server.URL, `{"remote_access_enabled":false}`)
	s := privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, false, s["remote_access_enabled"])
	assert.Equal(t, true, s["remote_access_configured"])
	assert.Equal(t, "disabled", s["remote_access_status"])
}

// #3355: the dashboard is told whether remote access is encrypted, and the mobile QR
// code which scheme other devices must use.
func TestRemoteAccessStatus_ReportsTLS(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	handler.cfg.Host = "0.0.0.0"
	s := privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, false, s["remote_access_tls"])
	net := networkInfoServer(t, handler)
	assert.Equal(t, "http", privacyGetJSON(t, net.URL+"/api/system/network-info")["scheme"])

	handler.cfg.TLS = true
	s = privacyGetJSON(t, server.URL+"/api/system/privacy")
	assert.Equal(t, true, s["remote_access_tls"])
	assert.Equal(t, "https", privacyGetJSON(t, net.URL+"/api/system/network-info")["scheme"])
}

// H5: the privacy status says who besides the system and administrators can change the agent tools
// folder, so Settings can warn about a portable Coral in the user's profile; an empty list, and no
// warning, for an install only an administrator can change.
func TestPrivacyStatusReportsWhoCanChangeTheToolsFolder(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	t.Cleanup(func() { agent.SetToolsFolderWriters("", nil) })
	agent.SetToolsFolderWriters(`C:\Users\me\Coral Portable`, []string{"this user (the folder's owner)", "this user"})
	tools := privacyGetJSON(t, server.URL+"/api/system/privacy")["tools_folder"].(map[string]any)
	assert.Equal(t, `C:\Users\me\Coral Portable`, tools["path"])
	assert.Equal(t, []any{"this user (the folder's owner)", "this user"}, tools["writable_by"])

	agent.SetToolsFolderWriters(`C:\Program Files\Coral`, nil)
	tools = privacyGetJSON(t, server.URL+"/api/system/privacy")["tools_folder"].(map[string]any)
	assert.Equal(t, []any{}, tools["writable_by"], "an empty list, never null")
}
