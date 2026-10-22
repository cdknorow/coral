package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5480: the operator's custom models. The set is replaced as a whole, validated as a whole; both catalogs the pickers read carry the customs after the CLI's models.

// customRig is a system handler whose model lists come from a stand-in fetcher: the real CLIs are never listed (nor found) by a test.
func customRig(t *testing.T) *SystemHandler {
	t.Helper()
	h := newSystemHandlerForTest(t)
	cli := filepath.Join(t.TempDir(), "cli")
	require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755))
	h.models = agentmodels.NewService(func(string) (string, error) { return cli, nil })
	h.models.SetFetcher(func(ctx context.Context, family, path string) ([]agentmodels.Model, string, error) {
		return []agentmodels.Model{{Value: "claude-opus-5-5", DisplayName: "Opus 5.5"}, {Value: "claude-sonnet-5-5", DisplayName: "Sonnet 5.5"}}, "account", nil
	})
	t.Cleanup(h.models.Wait)
	return h
}

func putCustom(t *testing.T, h *SystemHandler, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.PutCustomModels(rec, httptest.NewRequest(http.MethodPut, "/api/custom-models", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func getCustom(t *testing.T, h *SystemHandler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.GetCustomModels(rec, httptest.NewRequest(http.MethodGet, "/api/custom-models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestCustomModelsRoundTripWithTheLimitsSettingsValidatesAgainst(t *testing.T) {
	h := newSystemHandlerForTest(t)
	empty := getCustom(t, h)
	assert.Equal(t, []any{}, empty["models"], "never null")
	limits := empty["limits"].(map[string]any)
	assert.EqualValues(t, 50, limits["max_per_family"])
	assert.EqualValues(t, 200, limits["max_total"])
	assert.EqualValues(t, 128, limits["max_value_length"])
	efforts := limits["efforts"].(map[string]any)
	assert.Equal(t, []any{"low", "medium", "high", "xhigh", "max"}, efforts["claude"])
	assert.Equal(t, []any{"minimal", "low", "medium", "high", "xhigh"}, efforts["codex"])
	assert.Equal(t, []any{"low", "medium", "high", "xhigh", "max"}, efforts["agy"])
	assert.Equal(t, []any{}, efforts["pi"], "Pi takes no levels (not installed to check)")

	code, out := putCustom(t, h, `{"models":[{"family":"claude","value":"global-opus-5-5","display_name":" Global  Opus ","efforts":["low","high"]},{"family":"codex","value":"gpt-x"}]}`)
	require.Equal(t, http.StatusOK, code, out)
	models := out["models"].([]any)
	require.Len(t, models, 2)
	assert.Equal(t, "Global Opus", models[0].(map[string]any)["display_name"])
	assert.Equal(t, []any{"low", "high"}, models[0].(map[string]any)["efforts"])
	assert.Equal(t, getCustom(t, h)["models"], out["models"], "the same shape as GET")

	code, out = putCustom(t, h, `{"models":[]}`)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{}, getCustom(t, h)["models"], "an empty list clears them")
}

// A bad row names its place and field, and nothing at all changes.
func TestCustomModelsAreValidatedAsAWholeAndNothingChangesOnAnError(t *testing.T) {
	h := newSystemHandlerForTest(t)
	good := `{"models":[{"family":"claude","value":"keep-me"}]}`
	code, _ := putCustom(t, h, good)
	require.Equal(t, http.StatusOK, code)

	for body, want := range map[string]struct {
		row           int
		family, field string
	}{
		`{"models":[{"family":"claude","value":"a"},{"family":"codex","value":"--dangerous"}]}`:                        {1, "codex", "value"},
		`{"models":[{"family":"claude","value":"a b"}]}`:                                                               {0, "claude", "value"},
		`{"models":[{"family":"gemini","value":"a"}]}`:                                                                 {0, "gemini", "family"},
		`{"models":[{"family":"claude","value":"a","efforts":["turbo"]}]}`:                                             {0, "claude", "efforts"},
		`{"models":[{"family":"pi","value":"a","efforts":["low"]}]}`:                                                   {0, "pi", "efforts"},
		`{"models":[{"family":"claude","value":"a"},{"family":"claude","value":"b"},{"family":"claude","value":"a"}]}`: {2, "claude", "value"},
	} {
		code, out := putCustom(t, h, body)
		assert.Equal(t, http.StatusBadRequest, code, body)
		assert.EqualValues(t, want.row, out["row"], body)
		assert.Equal(t, want.family, out["family"], body)
		assert.Equal(t, want.field, out["field"], body)
		assert.NotEmpty(t, out["message"], body)
		assert.Contains(t, out["error"], "custom model", body)
		got := getCustom(t, h)["models"].([]any)
		require.Len(t, got, 1, "the earlier set stands: %s", body)
		assert.Equal(t, "keep-me", got[0].(map[string]any)["value"])
	}
	for _, body := range []string{`not json`, `{"models":"x"}`, `{"models":[` + strings.Repeat(`{"family":"claude","value":"a"},`, 10) + `]}`} {
		code, _ := putCustom(t, h, body)
		assert.Equal(t, http.StatusBadRequest, code, body)
	}
	assert.Len(t, getCustom(t, h)["models"], 1)
}

func TestCustomModelsRequestBodyIsCapped(t *testing.T) {
	h := newSystemHandlerForTest(t)
	big := `{"models":[{"family":"claude","value":"a","display_name":"` + strings.Repeat("x", 70<<10) + `"}]}`
	code, _ := putCustom(t, h, big)
	assert.Equal(t, http.StatusBadRequest, code)
}

// BOTH catalogs the pickers read carry the customs, after the CLI's own models: the curated list (launch, team, workflow step, defaults) and the CLI's list (the running agent's chip).
func TestCustomModelsReachBothCatalogs(t *testing.T) {
	h := customRig(t)
	code, out := putCustom(t, h, `{"models":[
		{"family":"claude","value":"global-opus-5-5","display_name":"Global Opus","efforts":["high","max"]},
		{"family":"claude","value":"claude-opus-5-5","display_name":"My Opus","efforts":["low"]},
		{"family":"agy","value":"agy-custom"},
		{"family":"codex","value":"gpt-x","efforts":["minimal"]},
		{"family":"pi","value":"pi-x"}]}`)
	require.Equal(t, http.StatusOK, code, out)

	// 1. GET /api/agent-models: ids, the curated ones first.
	rec := httptest.NewRecorder()
	h.GetAgentModels(rec, httptest.NewRequest(http.MethodGet, "/api/agent-models", nil))
	var curated map[string][]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &curated))
	assert.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1", "global-opus-5-5"}, curated["claude"], "after the curated ids, once each")
	assert.Contains(t, curated["codex"], "gpt-x")
	assert.Contains(t, curated["agy"], "agy-custom")
	assert.Contains(t, curated["pi"], "pi-x")
	assert.NotContains(t, curated["claude"], "gpt-x", "another provider's customs are not mixed in")
	for typ, ids := range curated {
		assert.NotNil(t, ids, typ)
	}

	// 2. GET /api/agents/models?type=: the model chip's list, with the flag and the levels the user set.
	list := func(typ string) []map[string]any {
		h.models.Get(typ) // starts the stand-in fetch
		h.models.Wait()
		rec := httptest.NewRecorder()
		h.GetAgentCLIModels(rec, httptest.NewRequest(http.MethodGet, "/api/agents/models?type="+typ, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var res struct {
			Models []map[string]any `json:"models"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		return res.Models
	}
	claude := list("claude")
	byValue := map[string]map[string]any{}
	for _, m := range claude {
		byValue[m["value"].(string)] = m
	}
	require.Contains(t, byValue, "global-opus-5-5")
	assert.Equal(t, true, byValue["global-opus-5-5"]["custom"])
	assert.Equal(t, []any{"high", "max"}, byValue["global-opus-5-5"]["efforts"])
	assert.Equal(t, "Global Opus", byValue["global-opus-5-5"]["display_name"])
	assert.Equal(t, "My Opus", byValue["claude-opus-5-5"]["display_name"], "a custom with a built-in id changes name and levels, never the value")
	assert.Equal(t, "claude-opus-5-5", byValue["claude-opus-5-5"]["value"])
	assert.Equal(t, claude[len(claude)-1]["value"], "global-opus-5-5", "after the CLI's models")
	_, hasFlag := byValue["claude-sonnet-5-5"]["custom"]
	assert.False(t, hasFlag, "a CLI model is not flagged")

	var gemini []string
	for _, m := range list("gemini") { // gemini runs agy: agy's customs
		gemini = append(gemini, m["value"].(string))
	}
	assert.Contains(t, gemini, "agy-custom")
	assert.NotContains(t, gemini, "global-opus-5-5")

	// Removing them removes them from both.
	putCustom(t, h, `{"models":[]}`)
	rec = httptest.NewRecorder()
	h.GetAgentModels(rec, httptest.NewRequest(http.MethodGet, "/api/agent-models", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &curated))
	assert.NotContains(t, curated["claude"], "global-opus-5-5")
	for _, m := range list("claude") {
		assert.NotEqual(t, "global-opus-5-5", m["value"])
	}
}

// A stored row is checked again when it is read: one that no longer passes (a hand-edited database) is served by neither catalog.
func TestACustomModelThatNoLongerPassesIsNeverServed(t *testing.T) {
	h := customRig(t)
	require.NoError(t, h.ss.ReplaceCustomModels(t.Context(), []store.CustomModel{
		{Family: "claude", Value: "fine", DisplayName: "Fine\x00\u202e name", Efforts: []string{"low"}},
		{Family: "claude", Value: "--dangerously-skip-permissions", DisplayName: "x"},
		{Family: "claude", Value: "has space"},
		{Family: "unknown", Value: "x"},
		{Family: "codex", Value: "bad-level", Efforts: []string{"--yolo"}},
		{Family: "agy", Value: "ok-agy"},
	}))
	got := getCustom(t, h)["models"].([]any)
	var values []string
	for _, m := range got {
		values = append(values, m.(map[string]any)["value"].(string))
	}
	assert.Equal(t, []string{"fine", "ok-agy"}, values)
	assert.Equal(t, "Fine name", got[0].(map[string]any)["display_name"], "a control or direction character in a name is dropped on read too")

	rec := httptest.NewRecorder()
	h.GetAgentModels(rec, httptest.NewRequest(http.MethodGet, "/api/agent-models", nil))
	assert.NotContains(t, rec.Body.String(), "dangerously")
	assert.NotContains(t, rec.Body.String(), "has space")
	rec = httptest.NewRecorder()
	h.GetAgentCLIModels(rec, httptest.NewRequest(http.MethodGet, "/api/agents/models?type=claude", nil))
	assert.NotContains(t, rec.Body.String(), "dangerously")
	assert.Contains(t, rec.Body.String(), `"fine"`)
}

// The writes are the operator's, never an agent's. Through Coral Hub they are the machine owner's (#5996): the tunnel rows say so and the handlers refuse anyone else.
func TestCustomModelRoutesAreTheOperatorsAndThroughTheHubTheOwners(t *testing.T) {
	for _, key := range []string{"GET /api/custom-models", "PUT /api/custom-models"} {
		_, ok := operatorOnlyRoutes[key]
		assert.True(t, ok, key+" is in the operator-only table")
	}
	_, mode := tunnel.OwnerRoute(http.MethodGet, "/api/custom-models")
	assert.Equal(t, tunnel.OwnerRefuse, mode, "the list is the owner's through the hub")
	_, mode = tunnel.OwnerRoute(http.MethodPut, "/api/custom-models")
	assert.Equal(t, tunnel.OwnerRefuse, mode, "the write is the owner's through the hub")
	assert.Equal(t, tunnel.TierView, tunnel.Classify(http.MethodGet, "/api/custom-models", ""), "the list is View")
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify(http.MethodPut, "/api/custom-models", ""), "the write is Launch")
}

func customCall(h *SystemHandler, method, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/custom-models", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.GetCustomModels(rec, req)
	} else {
		h.PutCustomModels(rec, req)
	}
	return rec
}

const oneCustomModel = `{"models":[{"family":"claude","value":"my-model","display_name":"Mine","efforts":[]}]}`

// Through Coral Hub the owner can read and replace the list; a grantee, even at launch tier, is refused with owner_only and nothing changes; this computer is as before.
func TestCustomModelsThroughTheHubAreTheOwnersAlone(t *testing.T) {
	withLabel(t, ownerLabel, nil)
	h := customRig(t)

	rec := customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusOK, rec.Code, "the owner reads the list through the hub")
	rec = customCall(h, http.MethodPut, oneCustomModel, ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusOK, rec.Code, "the owner writes it through the hub: "+rec.Body.String())
	assert.Len(t, getCustom(t, h)["models"], 1)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec = customCall(h, method, `{"models":[]}`, granteeHeaders())
		assert.Equal(t, http.StatusForbidden, rec.Code, method+" by a grantee")
		assert.Contains(t, rec.Body.String(), "owner_only", method+" by a grantee says why")
	}
	assert.Len(t, getCustom(t, h)["models"], 1, "a refused write changed nothing")

	assert.Equal(t, http.StatusOK, customCall(h, http.MethodGet, "", nil).Code, "this computer reads as before")
	assert.Equal(t, http.StatusOK, customCall(h, http.MethodPut, `{"models":[]}`, nil).Code, "this computer writes as before")
	assert.Len(t, getCustom(t, h)["models"], 0)
}

// A machine that has not recorded its owner's label refuses even the owner through the hub (the check fails closed, as for the other owner routes).
func TestCustomModelsThroughTheHubNeedTheRecordedOwnerLabel(t *testing.T) {
	withLabel(t, "", nil)
	h := customRig(t)
	assert.Equal(t, http.StatusForbidden, customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, http.StatusForbidden, customCall(h, http.MethodPut, oneCustomModel, ownerHeaders(ownerLabel)).Code)
}

// The switcher types "/model <id>" into the agent's terminal: an id with [ ] @ : / + and the longest one are typed exactly, and one that is not an id is refused with nothing typed.
func TestSwitchTypesACustomModelIdExactlyAndRefusesWhatIsNotAnId(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5[1m]", "vendor/model@2:high+x", "m" + strings.Repeat("a", 127)} {
		r := newSwitchRig(t, "claude")
		code, m := r.post(t, map[string]any{"model": model})
		require.Equal(t, http.StatusAccepted, code, m)
		r.waitTyped(t, "/model "+model)
	}
	r := newSwitchRig(t, "claude")
	for _, bad := range []string{"--dangerous", "a b", "a\nb", "m" + strings.Repeat("a", 128), "x;y"} {
		code, out := r.post(t, map[string]any{"model": bad})
		assert.Equal(t, http.StatusBadRequest, code, "%q: %s", bad, out)
	}
	assert.Empty(t, r.typed(), "nothing typed for a refused model")
}

// #5485 (Security): replace-all must not be a way to wipe the set by accident. A body without "models" (a typo, an empty object, null) is refused and the stored set stays.
func TestAPutWithoutTheModelsListChangesNothing(t *testing.T) {
	h := newSystemHandlerForTest(t)
	code, _ := putCustom(t, h, `{"models":[{"family":"claude","value":"keep-me"}]}`)
	require.Equal(t, http.StatusOK, code)
	for _, body := range []string{`{}`, `{"model":[{"family":"claude","value":"x"}]}`, `{"models":null}`, `null`, `[]`, `{"models":[],"extra":1}`} {
		code, _ := putCustom(t, h, body)
		assert.Equal(t, http.StatusBadRequest, code, body)
	}
	got := getCustom(t, h)
	models, _ := got["models"].([]any)
	require.Len(t, models, 1, "the stored set is untouched")
	assert.Equal(t, "keep-me", models[0].(map[string]any)["value"])
	code, _ = putCustom(t, h, `{"models":[]}`)
	assert.Equal(t, http.StatusOK, code, "an explicit empty list is how the set is cleared")
	assert.Empty(t, getCustom(t, h)["models"])
}

// #5910 (review of #5909): every agent type of a CLI family has a list in the catalog the launch dialog reads, the antigravity alias included: it runs agy, and the curated map
// has no entry of its own for it, so its picker was empty.
func TestAgentModelsCatalogHasAListForEveryTypeOfAFamily(t *testing.T) {
	h := customRig(t)
	get := func() map[string][]string {
		rec := httptest.NewRecorder()
		h.GetAgentModels(rec, httptest.NewRequest(http.MethodGet, "/api/agent-models", nil))
		var out map[string][]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	curated := get()
	require.NotEmpty(t, curated["agy"])
	for _, typ := range []string{"agy", "antigravity", "gemini"} {
		assert.Equal(t, curated["agy"], curated[typ], typ+" has agy's curated list")
	}

	code, body := putCustom(t, h, `{"models":[{"family":"agy","value":"agy-custom"},{"family":"codex","value":"gpt-x"}]}`)
	require.Equal(t, http.StatusOK, code, body)
	withCustom := get()
	for _, typ := range []string{"agy", "antigravity", "gemini"} {
		assert.Contains(t, withCustom[typ], "agy-custom", typ+" has the family's custom model")
		assert.Equal(t, 1, strings.Count(strings.Join(withCustom[typ], ","), "agy-custom"), typ+": once")
		assert.NotContains(t, withCustom[typ], "gpt-x", typ+": another provider's custom is not mixed in")
	}
	assert.NotContains(t, withCustom["claude"], "agy-custom")
}
