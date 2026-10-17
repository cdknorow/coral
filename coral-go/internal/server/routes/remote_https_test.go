package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4297 step 2: a server that is not registered as private must be https; a private one may use
// http, and an http URL at an address that is not this computer carries a warning in every view
// of the server. Rows registered before keep working.
func TestRemoteHTTPRules(t *testing.T) {
	_, err := validateNewRemoteURL("http://93.184.216.34:8420", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
	got, err := validateNewRemoteURL("https://93.184.216.34:8420", false)
	require.NoError(t, err)
	assert.Equal(t, "https://93.184.216.34:8420", got)
	got, err = validateNewRemoteURL("http://192.168.1.5:8420", true)
	require.NoError(t, err, "a private server may use http")
	assert.Equal(t, "http://192.168.1.5:8420", got)

	for raw, warn := range map[string]bool{
		"http://192.168.1.5:8420":    true,
		"http://93.184.216.34:8420":  true,
		"http://remote.example:8420": true,
		"http://10.0.0.2":            true,
		"https://192.168.1.5:8420":   false,
		"http://127.0.0.1:8420":      false,
		"http://127.1.2.3:8420":      false,
		"http://localhost:8420":      false,
		"http://LOCALHOST:8420":      false,
		"http://[::1]:8420":          false,
		"not a url\x7f":              false,
	} {
		w := remoteURLWarning(raw)
		if warn {
			assert.Equal(t, remoteHTTPWarning, w, raw)
		} else {
			assert.Empty(t, w, raw)
		}
	}
}

func TestRemoteHTTPThroughTheAPI(t *testing.T) {
	e := newRSEnv(t)
	ctx := context.Background()

	// Registering a public address over http is refused before anything is contacted.
	w := e.do("POST", "/api/servers", map[string]any{"id": "pub", "url": "http://93.184.216.34:8420", "api_key": routeSecret})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "https")

	// Rows that exist already (from before the rule, or private) keep working and carry the warning.
	rs := e.h.store
	_, err := rs.Add(ctx, "old", "Old", "http://93.184.216.34:8420", routeSecret, false)
	require.NoError(t, err)
	_, err = rs.Add(ctx, "lan", "LAN", "http://192.168.1.5:8420", routeSecret, true)
	require.NoError(t, err)
	_, err = rs.Add(ctx, "tls", "TLS", "https://93.184.216.34:8420", routeSecret, false)
	require.NoError(t, err)
	_, err = rs.Add(ctx, "self", "Self", "http://127.0.0.1:8420", routeSecret, true)
	require.NoError(t, err)

	w = e.do("GET", "/api/servers", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	warning := map[string]any{}
	for _, s := range list {
		warning[s["id"].(string)] = s["warning"]
	}
	assert.Equal(t, remoteHTTPWarning, warning["old"])
	assert.Equal(t, remoteHTTPWarning, warning["lan"])
	assert.Nil(t, warning["tls"])
	assert.Nil(t, warning["self"])
	assert.Nil(t, warning["local"])

	// An edit that doesn't change the URL is not refused for being http, and the answer carries the warning.
	w = e.do("PATCH", "/api/servers/old", map[string]any{"label": "Renamed"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), remoteHTTPWarning)
	// Moving it to another http address, or switching private off on a private http row, is.
	w = e.do("PATCH", "/api/servers/old", map[string]any{"url": "http://93.184.216.35:8420"})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = e.do("PATCH", "/api/servers/lan", map[string]any{"allow_private": false})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	// Test still runs on the old row (unreachable here, but not refused) and shows the warning.
	w = e.do("POST", "/api/servers/old/test", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), remoteHTTPWarning)
}
