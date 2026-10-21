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
