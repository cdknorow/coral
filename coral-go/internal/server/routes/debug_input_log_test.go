package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The "Log terminal input (debug)" setting is off by default and takes effect as
// soon as it is saved; the privacy status reports it so Settings can show that it
// is on (#3256).
func TestDebugInputLogSettingAppliesLive(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	inputlog.Setup(t.TempDir(), false)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })

	status := func() map[string]any {
		resp, err := http.Get(server.URL + "/api/system/privacy")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		log, _ := body["debug_input_log"].(map[string]any)
		require.NotNil(t, log, "privacy status reports debug_input_log")
		return log
	}
	put := func(v bool) {
		b, _ := json.Marshal(map[string]any{inputlog.SettingKey: v})
		req, _ := http.NewRequest("PUT", server.URL+"/api/settings", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	assert.Equal(t, false, status()["enabled"], "off by default")
	put(true)
	assert.True(t, inputlog.State().Enabled, "on as soon as it is saved")
	assert.Equal(t, true, status()["enabled"])
	assert.NotEmpty(t, status()["path"])
	put(false)
	assert.False(t, inputlog.State().Enabled, "off as soon as it is saved")
	assert.Equal(t, false, status()["enabled"])
}

// DELETE /api/system/debug-input-log removes the kept log, only from this machine and
// only while the log is off (#3256 review).
func TestDeleteDebugInputLog(t *testing.T) {
	server, handler := setupSystemTestServer(t)
	dir := t.TempDir()
	inputlog.Setup(dir, false)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	path := filepath.Join(dir, inputlog.FileName)
	del := func() *http.Response {
		req, _ := http.NewRequest("DELETE", server.URL+"/api/system/debug-input-log", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}

	inputlog.SetSetting(true)
	inputlog.Write("p", inputlog.Text, "x")
	inputlog.Flush()
	assert.Equal(t, http.StatusConflict, del().StatusCode, "refused while on")
	_, err := os.Stat(path)
	require.NoError(t, err)

	inputlog.SetSetting(false)
	assert.Equal(t, http.StatusOK, del().StatusCode)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "deleted")

	// From another machine (a LAN client with --remote): refused.
	require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o600))
	req := httptest.NewRequest("DELETE", "/api/system/debug-input-log", nil)
	req.RemoteAddr = "192.168.1.20:51515"
	rec := httptest.NewRecorder()
	handler.DeleteDebugInputLog(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	_, err = os.Stat(path)
	assert.NoError(t, err, "a refused request deletes nothing")

	// Through the Coral Hub tunnel (loopback peer, stamped by the connector): refused.
	req = httptest.NewRequest("DELETE", "/api/system/debug-input-log", nil)
	req.RemoteAddr = "127.0.0.1:51516"
	req.Header.Set(tunnel.ViaHeader, "1")
	rec = httptest.NewRecorder()
	handler.DeleteDebugInputLog(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a tunnel request is refused even from loopback")
	_, err = os.Stat(path)
	assert.NoError(t, err)
}
