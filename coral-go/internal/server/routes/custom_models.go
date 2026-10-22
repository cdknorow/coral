package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/store"
)

// Custom models (#5480): the model ids the operator adds in Settings > Custom models, per provider, with the thinking levels each takes. They are merged, at read time and after the
// CLI's own models, into BOTH catalogs the dashboard's pickers read: GET /api/agent-models (the curated list the launch, team, workflow-step and defaults pickers use) and
// GET /api/agents/models?type= (the CLI's list the running agent's model chip reads). A custom id reaches a command line as --model's one argument, so it passes
// agentmodels.ValidModelValue when it is saved and again whenever it is read (a row that no longer passes is not served).

// maxCustomModelsBody caps the request: 200 models of at most 128-character ids, 80-character names and a few levels fit with room to spare.
const maxCustomModelsBody = 64 << 10

type customModelsReply struct {
	Models []agentmodels.Custom `json:"models"`
	Limits customModelLimits    `json:"limits"`
}

type customModelLimits struct {
	Families     []string            `json:"families"`
	MaxPerFamily int                 `json:"max_per_family"`
	MaxTotal     int                 `json:"max_total"`
	MaxValueLen  int                 `json:"max_value_length"`
	MaxNameLen   int                 `json:"max_name_length"`
	Efforts      map[string][]string `json:"efforts"` // the levels each provider's CLI takes; empty: none
	ValueRule    string              `json:"value_rule"`
}

func customLimits() customModelLimits {
	efforts := map[string][]string{}
	for _, f := range agentmodels.CustomFamilies {
		efforts[f] = agentmodels.FamilyLevels(f)
	}
	return customModelLimits{
		Families: agentmodels.CustomFamilies, MaxPerFamily: agentmodels.MaxCustomPerFamily, MaxTotal: agentmodels.MaxCustomTotal,
		MaxValueLen: agentmodels.MaxCustomValueLen, MaxNameLen: 80, Efforts: efforts,
		ValueRule: "starts with a letter or digit; letters, digits and . _ : / @ [ ] + - only; no spaces",
	}
}

// loadCustomModels is the stored set, checked again: a row that no longer passes validation is left out.
func (h *SystemHandler) loadCustomModels(ctx context.Context) ([]agentmodels.Custom, error) {
	if h.ss == nil {
		return []agentmodels.Custom{}, nil
	}
	rows, err := h.ss.ListCustomModels(ctx)
	if err != nil {
		return nil, err
	}
	in := make([]agentmodels.Custom, 0, len(rows))
	for _, r := range rows {
		in = append(in, agentmodels.Custom{Family: r.Family, Value: r.Value, DisplayName: r.DisplayName, Efforts: r.Efforts})
	}
	return agentmodels.CleanCustom(in), nil
}

// GetCustomModels lists the custom models and the limits Settings validates against. GET /api/custom-models
func (h *SystemHandler) GetCustomModels(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5996)
		return
	}
	models, err := h.loadCustomModels(r.Context())
	if err != nil {
		errInternalServer(w, "the custom models could not be read")
		return
	}
	writeJSON(w, http.StatusOK, customModelsReply{Models: models, Limits: customLimits()})
}

// PutCustomModels replaces the whole set: the list is validated as a whole, in one transaction, and a problem names the row, the family and the field with nothing changed.
// PUT /api/custom-models {"models":[{"family","value","display_name","efforts"}]}
func (h *SystemHandler) PutCustomModels(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5996)
		return
	}
	// Replace-all must never be a way to wipe the set by accident (#5485): the body is an object with exactly one key, "models", whose value is a list (empty to clear). A body with no
	// "models" (a typo, {} or null) or with a key this route does not know is refused, so a misspelt "models" cannot clear everything.
	var raw map[string]json.RawMessage
	if err := decodeJSONMax(w, r, &raw, maxCustomModelsBody); err != nil {
		errBadRequest(w, "invalid JSON, or more than 64 KiB")
		return
	}
	var body struct{ Models []agentmodels.Custom }
	list, ok := raw["models"]
	if !ok || len(raw) != 1 || string(bytes.TrimSpace(list)) == "null" || json.Unmarshal(list, &body.Models) != nil {
		errBadRequest(w, `the body is {"models": [...]}: "models" is a required list (empty to clear) and the only field`)
		return
	}
	clean, err := agentmodels.ValidateCustomSet(body.Models)
	var ce *agentmodels.CustomError
	if errors.As(err, &ce) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ce.Error(), "row": ce.Row, "family": ce.Family, "field": ce.Field, "message": ce.Msg})
		return
	}
	rows := make([]store.CustomModel, 0, len(clean))
	for _, c := range clean {
		rows = append(rows, store.CustomModel{Family: c.Family, Value: c.Value, DisplayName: c.DisplayName, Efforts: c.Efforts})
	}
	if h.ss == nil {
		errInternalServer(w, "the custom models could not be saved")
		return
	}
	if err := h.ss.ReplaceCustomModels(r.Context(), rows); err != nil {
		errInternalServer(w, "the custom models could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, customModelsReply{Models: clean, Limits: customLimits()})
}
