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

// A folder Coral made private is a notice, never a warning (#295: the "Security warning" came back on every page load), and it stays in the status until the
// dashboard has shown it and said so: a CLI or a second tab that reads the status does not use it up.
func TestSystemStatusKeepsAFixedDataFolderNoticeUntilTheDashboardAcknowledgesIt(t *testing.T) {
	h := newSystemHandlerForTest(t)
	prevWarn, prevFixed, prevAck := dataDirWarning, dataDirFixed, ackDataDirFixed
	t.Cleanup(func() { dataDirWarning, dataDirFixed, ackDataDirFixed = prevWarn, prevFixed, prevAck })
	dataDirWarning = func(string) string { return "" }
	pending := "Coral made its data folder D: coral private to this user: Users could use it."
	dataDirFixed = func() string { return pending }
	ackDataDirFixed = func() { pending = "" }
	status := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	for i := 0; i < 3; i++ { // any number of other readers
		got := status()
		assert.Contains(t, got["security_notice"], "made its data folder", "read %d", i)
		assert.Equal(t, "", got["security_warning"], "a fixed folder is not a warning")
	}
	rec := httptest.NewRecorder()
	h.AckSecurityNotice(rec, httptest.NewRequest(http.MethodPost, "/api/system/security-notice/ack", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	got := status()
	assert.Equal(t, "", got["security_notice"], "after the dashboard says it has shown it")
	assert.Equal(t, "", got["security_warning"])
}
