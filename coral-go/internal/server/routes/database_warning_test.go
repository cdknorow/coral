package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4157: the system status carries the integrity check's notice for the dashboard, "" while
// nothing was found.
func TestSystemStatusCarriesTheDatabaseWarning(t *testing.T) {
	h := newSystemHandlerForTest(t)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	w, ok := out["database_warning"]
	require.True(t, ok, "the field is always present")
	assert.Equal(t, "", w)
}
