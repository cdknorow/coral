package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

func putSettings(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// Cases C6a and C6b of Security's #4180 fixture (#4176 rules 7, 16, 17, 18): only the Operator
// changes a team's health settings, never through Coral Hub; values are clamped; the recipient
// must be a member of this team.
func TestHealthSettingsAreTheOperatorsAndChecked(t *testing.T) {
	_, h := setupBoardTestServer(t)
	r := chi.NewRouter()
	r.Put("/api/board/{project}/settings", h.UpdateBoardSettings)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	ctx := context.Background()
	for _, s := range []struct{ id, title string }{{"Orchestrator", "Orchestrator"}, {"Dev", "Developer"}} {
		_, err := h.bs.Subscribe(ctx, "team", s.id, s.title, s.id+"-session", nil, "all")
		require.NoError(t, err)
	}
	_, err := h.bs.Subscribe(ctx, "other", "Other Team Lead", "Orchestrator", "olead-session", nil, "all")
	require.NoError(t, err)
	url := server.URL + "/api/board/team/settings"

	// C6a: an agent (the orchestrator included) can't change them; the Hub can't reach the PUT.
	for _, who := range []string{"Dev", "Orchestrator"} {
		code, _ := putSettings(t, url, map[string]any{"subscriber_id": who, "health": map[string]any{"on": false}})
		assert.Equal(t, http.StatusForbidden, code, who)
	}
	assert.True(t, h.bs.GetHealthSettings(ctx, "team").On, "settings unchanged")
	assert.Equal(t, tunnel.TierNever, tunnel.Classify(http.MethodPut, "/api/board/team/settings", ""), "no Hub route accepts the change")
	assert.NotEmpty(t, operatorOnlyRoutes["PUT /api/board/{project}/settings"], "an agent token is refused before the handler")

	// C6b: out-of-range values are clamped.
	code, out := putSettings(t, url, map[string]any{"subscriber_id": "Operator", "health": map[string]any{
		"on": true, "interval_min": 1, "claim_nudge": map[string]any{"on": true, "after_min": 0, "nudges": 50},
		"status_nudge": map[string]any{"on": true, "after_min": -3, "escalate_after_min": 2}, "mention_renudge": map[string]any{"on": true, "after_min": 1}}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got := h.bs.GetHealthSettings(ctx, "team")
	assert.Equal(t, 5, got.IntervalMin)
	assert.Equal(t, 5, got.ClaimNudge.AfterMin)
	assert.Equal(t, 5, got.ClaimNudge.Nudges)
	assert.Equal(t, 5, got.StatusNudge.AfterMin)
	assert.Equal(t, 5, got.StatusNudge.EscalateAfterMin)
	assert.Equal(t, 5, got.MentionRenudge.AfterMin)

	// The recipient: not a member, another team's agent: refused; a member: accepted.
	for _, rcpt := range []string{"Ghost", "Other Team Lead"} {
		hs := board.DefaultHealthSettings()
		hs.Recipient = rcpt
		code, _ := putSettings(t, url, map[string]any{"subscriber_id": "Operator", "health": hs})
		assert.Equal(t, http.StatusBadRequest, code, rcpt)
	}
	hs := board.DefaultHealthSettings()
	hs.Recipient = "Dev"
	code, _ = putSettings(t, url, map[string]any{"subscriber_id": "Operator", "health": hs})
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Dev", h.bs.GetHealthSettings(ctx, "team").Recipient)
}

// Security's MUST 2 on step 2 (#4176 rule 16): the handler refuses an agent itself, whatever
// subscriber_id it gives, and does not rely on the guard in front of the route.
func TestHealthSettingsHandlerRefusesAnAgentCaller(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "claude-a", nil, "all")
	require.NoError(t, err)
	put := func(mod func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodPut, "/api/board/team/settings", bytes.NewReader([]byte(`{"subscriber_id":"Operator","health":{"on":false}}`)))
		req.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "team")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		mod(req)
		rec := httptest.NewRecorder()
		h.UpdateBoardSettings(rec, req)
		return rec.Code
	}
	// An agent token claiming to be the Operator.
	code := put(func(r *http.Request) {
		*r = *r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "sid-a", SessionName: "claude-a"}))
	})
	assert.Equal(t, http.StatusForbidden, code, "agent token")
	// The agent command line without the Operator's signature.
	code = put(func(r *http.Request) { r.Header.Set("User-Agent", "coral-board/1.0") })
	assert.Equal(t, http.StatusForbidden, code, "unsigned agent command line")
	assert.True(t, h.bs.GetHealthSettings(ctx, "team").On, "settings unchanged")
	// The dashboard-shaped request still works.
	code = put(func(r *http.Request) { r.Header.Set("User-Agent", "Mozilla/5.0") })
	assert.Equal(t, http.StatusOK, code, "dashboard")
	assert.False(t, h.bs.GetHealthSettings(ctx, "team").On)
}

// Security's SHOULD (a): through Coral Hub the settings leave the health object out.
func TestBoardSettingsThroughTheHubLeaveOutHealth(t *testing.T) {
	_, h := setupBoardTestServer(t)
	get := func(hub bool) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/board/team/settings", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "team")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		if hub {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.GetBoardSettings(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	assert.Contains(t, get(false), "health")
	hub := get(true)
	assert.NotContains(t, hub, "health")
	assert.NotEmpty(t, hub, "the rest of the settings are still returned")
}

// Only a member of the board can register a question for the Operator.
func TestOperatorQuestionNeedsAMember(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "claude-a", nil, "all")
	require.NoError(t, err)
	register := func(who string) int {
		body := []byte(`{"subscriber_id":"` + who + `","wait_type":"operator","reason":"ship it?"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/board/team/waits", bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "team")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h.RegisterWait(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusBadRequest, register("Ghost"))
	assert.Equal(t, http.StatusCreated, register("Dev"))
	qs, err := h.bs.ListOperatorQuestions(ctx, "team")
	require.NoError(t, err)
	assert.Len(t, qs, 1)
}
