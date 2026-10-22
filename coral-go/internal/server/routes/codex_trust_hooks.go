// "Trust Coral's hooks in Codex" (#153): the local switch for agent.CodexHookTrust.

package routes

import (
	"context"
	"net/http"

	"github.com/subgentic/coral/internal/agent"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// CodexTrustHooksSetting is the switch: on unless set to "false". On, Coral trusts its own hooks for each Codex it
// launches, and only while nobody but an administrator can change them (agent.CodexHookTrust). Off, Codex asks the person
// to review them, as it does any hook. Local only, like the Coral Hub switches: written only by its own route, which the
// tunnel table doesn't have, and which refuses a tunneled request and a caller on this computer that isn't the dashboard
// or the signed command line.
const CodexTrustHooksSetting = setreg.CodexTrustCoralHooks

// CodexTrustFromSettings is the switch agent.SetCodexHookTrust reads on each launch. A settings read that fails is off:
// Codex then asks, as it would without Coral.
func CodexTrustFromSettings(ss *store.SessionStore) func() bool {
	return func() bool {
		s, err := ss.GetSettings(context.Background())
		return err == nil && setreg.Bool(s, CodexTrustHooksSetting)
	}
}

// codexTrustStatus is what the settings page shows: the switch, and whether Coral trusts its hooks now and why not.
func (h *CloudHandler) codexTrustStatus(r *http.Request) map[string]any {
	on := true
	if s, err := h.ss.GetSettings(r.Context()); err == nil {
		on = setreg.Bool(s, CodexTrustHooksSetting)
	}
	trusted, why := agent.CodexHookTrust()
	return map[string]any{"trust": on, "trusted": trusted, "why": why}
}

// CodexTrustHooks: GET /api/system/codex-trust-hooks. The switch and whether it applies now.
func (h *CloudHandler) CodexTrustHooks(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) {
		return
	}
	writeJSON(w, http.StatusOK, h.codexTrustStatus(r))
}

// SetCodexTrustHooks: PUT /api/system/codex-trust-hooks {"trust": bool}. Local dashboard or the signed command line only.
// It applies to Codex agents launched after the change.
func (h *CloudHandler) SetCodexTrustHooks(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Trust *bool `json:"trust"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Trust == nil {
		errBadRequest(w, "trust is required")
		return
	}
	v := "true"
	if !*body.Trust {
		v = "false"
	}
	if err := h.ss.SetSetting(r.Context(), CodexTrustHooksSetting, v); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	writeJSON(w, http.StatusOK, h.codexTrustStatus(r))
}
