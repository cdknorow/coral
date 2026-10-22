package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3637, Security's addition: a data folder others can read is reported on the dashboard's first
// page through the system status, not only in the log; a private folder adds nothing.
func TestSystemStatusCarriesTheDataFolderWarning(t *testing.T) {
	h := newSystemHandlerForTest(t)
	prev := dataDirWarning
	t.Cleanup(func() { dataDirWarning = prev })
	status := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	const warning = "Coral's data folder C:\\coral can be read by Everyone. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security)."
	dataDirWarning = func(string) string { return warning }
	assert.Equal(t, warning, status()["security_warning"])

	dataDirWarning = func(string) string { return "" }
	assert.Equal(t, "", status()["security_warning"], "a private folder: nothing to show")
}

// A folder Coral made private is told once, as a notice, and is never a warning (#295: the "Security warning" came back on every page load).
func TestSystemStatusTellsAFixedDataFolderOnce(t *testing.T) {
	h := newSystemHandlerForTest(t)
	prevWarn, prevFixed := dataDirWarning, dataDirFixed
	t.Cleanup(func() { dataDirWarning, dataDirFixed = prevWarn, prevFixed })
	dataDirWarning = func(string) string { return "" }
	pending := "Coral made its data folder D: coral private to this user: Users could use it."
	dataDirFixed = func() string { s := pending; pending = ""; return s }
	status := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	first := status()
	assert.Contains(t, first["security_notice"], "made its data folder")
	assert.Equal(t, "", first["security_warning"], "a fixed folder is not a warning")
	second := status()
	assert.Equal(t, "", second["security_notice"], "told once")
	assert.Equal(t, "", second["security_warning"])
}
