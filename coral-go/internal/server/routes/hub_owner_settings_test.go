package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5307: through Coral Hub the machine's OWNER (the stamped owner marker and the enrolled label) keeps a board's health setup in GET settings, can
// save it (the health part only), and can read the prompt inspection. A grantee, even at launch tier, gets none of it; this computer is as before.

const ownerLabel = "hub-user-1234abcd"

func ownerHeaders(user string) map[string]string {
	return map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: user}
}

func granteeHeaders() map[string]string {
	return map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: "hub-user-9999ffff"}
}

func withLabel(t *testing.T, label string, err error) {
	t.Helper()
	old := hubOwnerLabel
	hubOwnerLabel = func(context.Context) (string, error) { return label, err }
	t.Cleanup(func() { hubOwnerLabel = old })
}

func boardCall(t *testing.T, h *BoardHandler, method, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/board/team/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "team")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.GetBoardSettings(rec, req)
	} else {
		h.UpdateBoardSettings(rec, req)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func boardCallRaw(t *testing.T, h *BoardHandler, method, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/board/team/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "team")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.GetBoardSettings(rec, req)
	} else {
		h.UpdateBoardSettings(rec, req)
	}
	return rec
}

func TestOwnerKeepsHealthInBoardSettingsThroughTheHubAndGranteesDoNot(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "dev-session", nil, "all")
	require.NoError(t, err)

	code, out := boardCall(t, h, http.MethodGet, "", nil)
	require.Equal(t, 200, code)
	assert.Contains(t, out, "health", "this computer sees it")

	code, out = boardCall(t, h, http.MethodGet, "", ownerHeaders(ownerLabel))
	require.Equal(t, 200, code)
	assert.Contains(t, out, "health", "the owner through the hub keeps it")

	forgedVia := ownerHeaders(ownerLabel)
	forgedVia[tunnel.ViaHeader] = "1"
	noMarker := ownerHeaders(ownerLabel)
	delete(noMarker, tunnel.HubRoleHeader)
	otherLabel := ownerHeaders("hub-user-9999ffff")
	for name, hdr := range map[string]map[string]string{"a grantee": granteeHeaders(), "a forged stamp": forgedVia, "no marker": noMarker, "another label": otherLabel} {
		code, out = boardCall(t, h, http.MethodGet, "", hdr)
		require.Equal(t, 200, code, name)
		assert.NotContains(t, out, "health", name)
		assert.Contains(t, out, "cross_team_messaging", name+": the rest is unchanged")
	}
	// An unreadable enrolment record is no owner.
	withLabel(t, "", errors.New("db closed"))
	_, out = boardCall(t, h, http.MethodGet, "", ownerHeaders(ownerLabel))
	assert.NotContains(t, out, "health")
}

func TestOwnerSavesTheHealthSetupThroughTheHubOnly(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "dev-session", nil, "all")
	require.NoError(t, err)
	require.NoError(t, h.bs.SetHealthOn(ctx, "team", true))
	hs := board.DefaultHealthSettings()
	hs.Recipient = "Dev"
	hs.On = false
	body, _ := json.Marshal(map[string]any{"subscriber_id": "Operator", "health": hs})

	// A grantee at launch tier: refused, nothing saved.
	for name, hdr := range map[string]map[string]string{"grantee": granteeHeaders(), "other label": ownerHeaders("hub-user-9999ffff")} {
		code, out := boardCall(t, h, http.MethodPut, string(body), hdr)
		assert.Equal(t, 403, code, name)
		assert.Equal(t, "owner_only", out["code"], name)
		assert.True(t, h.bs.GetHealthSettings(ctx, "team").On, name+": nothing was saved")
	}
	// The owner: saved.
	code, out := boardCall(t, h, http.MethodPut, string(body), ownerHeaders(ownerLabel))
	require.Equal(t, 200, code, "%v", out)
	got := h.bs.GetHealthSettings(ctx, "team")
	assert.False(t, got.On)
	assert.Equal(t, "Dev", got.Recipient)
	// Same checks as for this computer: the recipient must be a member.
	bad := board.DefaultHealthSettings()
	bad.Recipient = "Ghost"
	badBody, _ := json.Marshal(map[string]any{"subscriber_id": "Operator", "health": bad})
	code, _ = boardCall(t, h, http.MethodPut, string(badBody), ownerHeaders(ownerLabel))
	assert.Equal(t, 400, code)
}

func TestTheOwnerThroughTheHubCannotChangeTheCrossTeamFields(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()
	before, err := h.bs.GetBoardSettings(ctx, "team")
	require.NoError(t, err)
	for _, field := range []string{`"cross_team_messaging": true`, `"cross_team_servers": ["srv_abc"]`, `"cross_team_artifacts": true`,
		`"cross_team_messaging": true, "health": {"on": false}`} {
		code, out := boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator",`+field+`}`, ownerHeaders(ownerLabel))
		assert.Equal(t, 403, code, field)
		assert.Equal(t, "local_only", out["code"], field)
	}
	after, err := h.bs.GetBoardSettings(ctx, "team")
	require.NoError(t, err)
	assert.Equal(t, before, after, "nothing of a refused request is saved, the health part included")
	// This computer is as before.
	code, _ := boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","cross_team_messaging":true}`, nil)
	assert.Equal(t, 200, code)
}

func TestPromptInspectionThroughTheHubIsTheOwnersRead(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/c.db")
	require.NoError(t, err)
	defer db.Close()
	_ = store.NewSessionStore(db)
	h := NewSystemHandler(db, nil)
	withLabel(t, ownerLabel, nil)
	get := func(hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/settings/prompt-inspection?board=team", bytes.NewReader(nil))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.GetPromptInspection(rec, req)
		return rec.Code
	}
	assert.Equal(t, 200, get(nil), "this computer")
	assert.Equal(t, 200, get(ownerHeaders(ownerLabel)), "the owner")
	assert.Equal(t, 403, get(granteeHeaders()), "a grantee, even at launch tier")
	assert.Equal(t, 403, get(ownerHeaders("hub-user-9999ffff")), "another label")
	forged := ownerHeaders(ownerLabel)
	forged[tunnel.ViaHeader] = "1"
	assert.Equal(t, 403, get(forged), "a forged stamp")
}

func TestTheOwnerRoutesAreTheTunnelsLaunchRowsWithTheBoardQuery(t *testing.T) {
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("PUT", "/api/board/team/settings", ""))
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("GET", "/api/settings/prompt-inspection", ""))
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("GET", "/api/settings/prompt-inspection", "board=team"))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", "/api/settings/prompt-inspection", "x=1"), "only the board query")
	assert.Equal(t, tunnel.TierView, tunnel.Classify("GET", "/api/board/team/settings", ""), "grantees still read the settings")
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("POST", "/api/board/team/settings", ""))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("DELETE", "/api/board/team/settings", ""))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/settings", ""), "PUT /api/settings stays never")
}

// coral-hub Security's rules (#5307): the owner label must be recorded and verified (the marker alone is not enough here), the replies are not
// cacheable, the save is strict JSON of known keys under a size cap, and a viewer at any tier without the owner marker is refused on each route.
func TestWithoutARecordedOwnerLabelTheConfigurationRoutesRefuseEveryone(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, "", nil) // enrolled before the label was recorded
	_, out := boardCall(t, h, http.MethodGet, "", ownerHeaders(ownerLabel))
	assert.NotContains(t, out, "health", "the marker alone does not keep the health setup")
	code, out := boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","health":{"on":false}}`, ownerHeaders(ownerLabel))
	assert.Equal(t, 403, code)
	assert.Equal(t, "owner_label_missing", out["code"], "the owner of a computer with no recorded label is told that is the reason (#6108)")

	db, err := storetest.Open(t, t.TempDir()+"/c.db")
	require.NoError(t, err)
	defer db.Close()
	sys := NewSystemHandler(db, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/prompt-inspection", nil)
	for k, v := range ownerHeaders(ownerLabel) {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	sys.GetPromptInspection(rec, req)
	assert.Equal(t, 403, rec.Code)
	// This computer is unaffected.
	assert.Equal(t, 200, boardCallRaw(t, h, http.MethodGet, "", nil).Code)
}

func TestTheOwnersSaveIsStrictAndTheRepliesAreNotCacheable(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "dev-session", nil, "all")
	require.NoError(t, err)
	require.NoError(t, h.bs.SetHealthOn(ctx, "team", true))
	for name, body := range map[string]string{
		"an unknown key":        `{"subscriber_id":"Operator","health":{"on":false},"theme":"dark"}`,
		"an unknown health key": `{"subscriber_id":"Operator","health":{"on":false,"restart_agents":true}}`,
		"an unknown nested key": `{"subscriber_id":"Operator","health":{"claim_nudge":{"on":true,"kill":true}}}`,
		"two objects":           `{"subscriber_id":"Operator","health":{"on":false}}{"health":{"on":true}}`,
		"trailing text":         `{"subscriber_id":"Operator","health":{"on":false}} x`,
		"not an object":         `[1]`,
		"empty":                 ``,
	} {
		rec := boardCallRaw(t, h, http.MethodPut, body, ownerHeaders(ownerLabel))
		assert.Equal(t, 400, rec.Code, name)
		assert.True(t, h.bs.GetHealthSettings(ctx, "team").On, name+": nothing was saved")
	}
	// Too large is its own answer (413 too_large), whatever is in the body.
	big := boardCallRaw(t, h, http.MethodPut, `{"subscriber_id":"Operator","health":{"on":false}}`+strings.Repeat(" ", boardSettingsHubMax), ownerHeaders(ownerLabel))
	assert.Equal(t, 413, big.Code)
	assert.Contains(t, big.Body.String(), `"too_large"`)
	assert.True(t, h.bs.GetHealthSettings(ctx, "team").On, "oversize: nothing was saved")
	// The same strict body is not asked of this computer's dashboard.
	assert.Equal(t, 200, boardCallRaw(t, h, http.MethodPut, `{"subscriber_id":"Operator","health":{"on":true},"extra":1}`, nil).Code)

	ok := boardCallRaw(t, h, http.MethodPut, `{"subscriber_id":"Operator","health":{"on":false}}`, ownerHeaders(ownerLabel))
	require.Equal(t, 200, ok.Code, ok.Body.String())
	assert.Equal(t, "no-store", ok.Header().Get("Cache-Control"))
	assert.Equal(t, "no-store", boardCallRaw(t, h, http.MethodGet, "", ownerHeaders(ownerLabel)).Header().Get("Cache-Control"))
	assert.Equal(t, "no-store", boardCallRaw(t, h, http.MethodGet, "", granteeHeaders()).Header().Get("Cache-Control"), "every reply through the hub")
	assert.Empty(t, boardCallRaw(t, h, http.MethodGet, "", nil).Header().Get("Cache-Control"), "this computer is as before")
}

func TestPromptInspectionThroughTheHubIsNoStoreAndBounded(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/c.db")
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	withLabel(t, ownerLabel, nil)
	get := func(query string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/settings/prompt-inspection"+query, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.GetPromptInspection(rec, req)
		return rec
	}
	rec := get("?board=team", ownerHeaders(ownerLabel))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Less(t, rec.Body.Len(), promptInspectionHubMax)
	assert.Equal(t, 400, get("?board="+strings.Repeat("b", promptInspectionBoardMax+1), ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, 200, get("?board="+strings.Repeat("b", promptInspectionBoardMax), ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, 403, get("", granteeHeaders()).Code, "a grantee at any tier")
	assert.Empty(t, get("?board=team", nil).Header().Get("Cache-Control"), "this computer is as before")
}
