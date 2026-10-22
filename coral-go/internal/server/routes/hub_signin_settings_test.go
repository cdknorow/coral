package routes

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// Security's #3966 condition 4: the generic settings PUT never writes Sign in with Coral Hub's
// settings, in any spelling and alongside other keys; only /api/auth/hub-signin and
// /api/auth/hub-origins do.
func TestPutSettingsRefusesHubSigninSettings(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	for _, k := range []string{"hub_signin_enabled", "hub_signin_origins", "hub_signin_offer_dismissed", "HUB_SIGNIN_ENABLED", " hub_signin_enabled "} {
		body := fmt.Sprintf(`{"theme":"dark",%q:"true"}`, k)
		r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.PutSettings(rec, r)
		assert.Equal(t, 400, rec.Code, k)
	}
	s, _ := store.NewSessionStore(db).GetSettings(t.Context())
	for k := range s {
		assert.False(t, strings.Contains(strings.ToLower(k), "hub_signin"), k)
	}
	assert.Empty(t, s["theme"], "nothing in a refused request is saved")
}

// #3966 round 1 (Security's must-fix): the Coral Hub link's trust settings are written only by
// their own routes, which a Coral Hub sign-in can't reach; the generic PUT refuses them for
// every caller, a Hub session included, and stores nothing of the request.
func TestPutSettingsRefusesHubTrustSettings(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	keys := []string{MaxModeSetting, AllowHubAutoAcceptSetting, RedactTokensSetting, HubRelaunchSetting, CodexTrustHooksSetting}
	assert.Len(t, hubTrustSettingRoutes, len(keys), "the census: every trust key is listed here")
	for _, k := range append(keys, "CLOUD_MAX_MODE", " hub_redact_tokens ", " HUB_RELAUNCH_UPDATES ", " Codex_Trust_Coral_Hooks ") {
		body := fmt.Sprintf(`{"theme":"dark",%q:"launch"}`, k)
		r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.PutSettings(rec, r)
		assert.Equal(t, 400, rec.Code, k)
		assert.Regexp(t, `/api/(cloud|system)/`, rec.Body.String(), k)
	}
	s, _ := store.NewSessionStore(db).GetSettings(t.Context())
	for _, k := range keys {
		assert.Empty(t, s[k], k)
	}
	assert.Empty(t, s["theme"])
}
