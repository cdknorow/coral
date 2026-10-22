//go:build !relaunchfixture

package routes

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #5452, Security's condition 6: a build without the relaunchfixture tag has the relaunch feature off (it has the real updater, which the routes do not reach), so both routes answer 404 as they always did, whatever
// the environment says (CORAL_RELAUNCH_FIXTURE_DIR is read by no code of this build).
func TestADefaultBuildHasTheRelaunchFeatureOffAndNoUpdater(t *testing.T) {
	assert.False(t, updateRelaunchFeature)
	assert.NotNil(t, BuildRelaunchUpdater(t.TempDir(), nil), "the real updater is built into every release and reached by nothing while the feature is off")

	t.Setenv("CORAL_RELAUNCH_FIXTURE_DIR", t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	h := NewUpdateRelaunchHandler(store.NewSessionStore(db), t.TempDir(), func() string { return "pty" }, BuildRelaunchUpdater(t.TempDir(), nil))
	for name, fn := range map[string]http.HandlerFunc{"GET /api/system/update": h.Status, "POST /api/system/update/relaunch": h.Relaunch} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/api/system/update", nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, name)
	}
}
