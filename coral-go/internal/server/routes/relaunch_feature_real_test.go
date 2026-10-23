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

// #6290: a build without the relaunchfixture tag has the relaunch feature on with the real updater. The routes answer (not 404), and with the Hub's switch at its default (off) the Hub's owner
// is refused, so nothing installs unless a person at this computer asks.
func TestADefaultBuildHasTheRelaunchFeatureOnWithTheRealUpdaterAndTheHubSwitchOff(t *testing.T) {
	assert.True(t, updateRelaunchFeature)
	u := BuildRelaunchUpdater(t.TempDir(), nil)
	require.NotNil(t, u)
	_, isReal := u.(*realUpdater)
	assert.True(t, isReal, "the real updater, never the fixture's")

	t.Setenv("CORAL_RELAUNCH_FIXTURE_DIR", t.TempDir()) // read by no code of this build
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	h := NewUpdateRelaunchHandler(store.NewSessionStore(db), t.TempDir(), func() string { return "pty" }, u)
	h.Enabled = func() bool { return updateRelaunchFeature }
	rec := httptest.NewRecorder()
	h.Relaunch(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/relaunch", nil))
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "the route is there")
	assert.Equal(t, http.StatusForbidden, rec.Code, "a request that is not the Hub's owner is refused")
}
