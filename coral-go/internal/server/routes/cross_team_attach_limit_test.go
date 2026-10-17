package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/httputil"
)

// The hourly attach limit is an action limit, not an overloaded server: its 429 carries the marker, so the
// dashboard's pollers do not pause on it (the 429 census), with the body every attach refusal has.
func TestAttachHourlyLimitIs429WithTheActionMarker(t *testing.T) {
	e := newAttachEnv(t, true)
	d := e.upload(alphaOrchSID, "alpha's report")
	st := storeFor(e.coralDir)
	for i := 0; i < uploadWindowCount; i++ {
		st.admitUpload("session:" + alphaOrchSID) // the window is used up
	}
	b, _ := json.Marshal(map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "here you go", "attach": []string{uri(d)}})
	req, _ := http.NewRequest("POST", e.url+"/api/board/beta/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Session", alphaOrchSID)
	req.Header.Set("X-Test-Name", "Orchestrator")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, out)
	assert.Equal(t, "action", resp.Header.Get(httputil.ActionLimitHeader))
	assert.Equal(t, "attach_limit", out["code"])
	assert.Contains(t, out["error"], "hourly upload limit")
}
