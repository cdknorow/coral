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

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/tunnel"
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
