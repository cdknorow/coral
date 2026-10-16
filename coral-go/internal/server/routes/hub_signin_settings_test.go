package routes

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
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
