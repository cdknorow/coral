package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #4489 (Security's #4488 SHOULD): GET /api/system/artifact-store lists the digests of the received files no board lists (received.unlisted.items) only to
// the Operator. A digest is enough to read the file through GET /api/artifacts/{id}, so an agent on board B could read a file sent to board A whose message
// never came. Everyone keeps the counts and sizes; the kept list was already the Operator's.
func TestUnlistedReceivedDigestsAreTheOperatorsOnly(t *testing.T) {
	rig := newArtifactRig(t)
	e := newArtifactEnv(t)
	e.dir, e.h.coralDir = rig.coralDir, rig.coralDir
	e.now = time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC) // long expired against the handler's clock: the message never came
	e.open()
	setReceivedCap(e, 1<<20)
	data := []byte("a file whose message never came\n")
	status, out := e.sendUnder(relaySrc, "never", data)
	require.Equal(t, 200, status, out)
	digest := hexOf(data)

	get := func(path string, hdr map[string]string) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodGet, rig.srv.URL+path, nil)
		require.NoError(t, err)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	unlisted := func(body map[string]any) map[string]any {
		r, _ := body["received"].(map[string]any)
		require.NotNil(t, r, "%v", body)
		u, _ := r["unlisted"].(map[string]any)
		require.NotNil(t, u, "%v", r)
		return u
	}

	code, body := get("/api/system/artifact-store", map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, code)
	u := unlisted(body)
	assert.EqualValues(t, 1, u["count"])
	items, _ := u["items"].([]any)
	require.Len(t, items, 1, "the Operator is told which file: %v", u)
	assert.Equal(t, digest, items[0].(map[string]any)["id"])

	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"}} {
		code, body := get("/api/system/artifact-store", hdr)
		if code != http.StatusOK { // a caller the identity layer turns away at the door sees nothing either
			assert.NotContains(t, body, "received", name)
			continue
		}
		u := unlisted(body)
		assert.EqualValues(t, 1, u["count"], name+": the count stays")
		assert.EqualValues(t, len(data), u["bytes"], name+": the size stays")
		assert.Empty(t, u["items"], name+": no digests")
		raw, _ := json.Marshal(body)
		assert.NotContains(t, string(raw), digest, name+": the digest is nowhere in the answer")
		// ...also with the detail view
		_, detail := get("/api/system/artifact-store?detail=1", hdr)
		raw, _ = json.Marshal(detail)
		assert.NotContains(t, string(raw), digest, name+": nor in the detail view")
	}

	// The kept list: ids for the Operator, a refusal (and no ids) for everyone else.
	code, kept := get("/api/system/artifact-store/kept", map[string]string{testDashboardHeader: "1"})
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, kept, "kept")
	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"}} {
		code, body := get("/api/system/artifact-store/kept", hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.NotContains(t, body, "kept", name)
	}
}
