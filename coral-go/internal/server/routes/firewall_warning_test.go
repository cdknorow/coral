package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/firewall"
)

// #97: a broad Windows Firewall rule for Coral's programs, found by the startup check, is on the dashboard's first page through the system
// status, as its own field next to security_warning; nothing found adds an empty one.
func TestSystemStatusCarriesTheFirewallWarning(t *testing.T) {
	h := newSystemHandlerForTest(t)
	t.Cleanup(func() { firewall.Set(nil) })
	status := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	assert.Equal(t, "", status()["firewall_warning"], "nothing found: nothing to show")

	r, ok := firewall.ParseRule("TCP Query User{G}C:\\Program Files\\Coral\\coral.exe",
		`v2.10|Action=Allow|Active=TRUE|Dir=In|Protocol=6|Profile=Public|App=C:\Program Files\Coral\coral.exe|Name=coral|`)
	require.True(t, ok)
	firewall.Set(firewall.Assess([]firewall.Rule{r}, `C:\Program Files\Coral\coral.exe`))
	got, _ := status()["firewall_warning"].(string)
	assert.Contains(t, got, "Set-NetFirewallRule -Name 'TCP Query User{G}C:\\Program Files\\Coral\\coral.exe' -Profile Private")
	assert.Contains(t, got, "Coral never makes firewall rules")
}
