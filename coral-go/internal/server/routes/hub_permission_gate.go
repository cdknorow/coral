// What a Coral Hub request may ask an agent to skip (#126 F4).

package routes

import (
	"net/http"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// hubAutoAcceptOff is the hub_auto_accept_disabled refusal: what is off, and where on this computer
// the owner turns it on.
const hubAutoAcceptOff = `Coral Hub auto-accept is turned off on this computer. To turn it on, open Settings › General › About › "Connect to Coral Hub…" on this computer, set "Most Coral Hub may do on this computer" to Launch, and turn on "Coral Hub can turn on auto-accept".`

// hubAsksToSkip reports whether a launch, team member or restart asks for an agent that acts
// without asking: flags that skip permission prompts (agent.SkipsPermissionPrompts, an allowlist),
// capability allow rules, allowed tools or custom hooks (the commands its CLI would run itself),
// the same choices limitAgentLaunch takes from an agent (#4032). Deny rules only narrow.
func hubAsksToSkip(agentType string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}) bool {
	return agent.SkipsPermissionPrompts(agentType, flags) || caps != nil && len(caps.Allow) > 0 || len(tools) > 0 || len(hooks) > 0
}

// refuseHubSkip answers a Coral Hub request that asks to skip permission prompts (skips) unless the
// owner allowed that on this computer, and reports whether it refused: the agent auto-accept rule
// (#3182, #3377 SEC-JB-01), on every route that starts an agent with the request's choices (launch,
// launch-team, restart, tasks/run). The request must carry the Hub's provenance, the local switch
// "Coral Hub can turn on auto-accept" must be on (off unless the owner turned it on) and the Launch
// mode must be set. It is refused, never stripped, so the person sees why. A local request is not
// gated: the owner chooses at the dashboard, the command line or with the API key.
func refuseHubSkip(w http.ResponseWriter, r *http.Request, ss *store.SessionStore, skips bool) bool {
	if !skips || !cloud.Tunneled(r) {
		return false
	}
	settings, err := ss.GetSettings(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return true
	}
	switch {
	case !cloud.TunneledAuthentic(r):
		refuseAutoAccept(w, http.StatusForbidden, "no_dashboard_provenance", "Auto-accept can only be turned on from the Coral dashboard or the Coral Hub")
	case !HubAutoAcceptAllowed(settings):
		refuseAutoAccept(w, http.StatusForbidden, "hub_auto_accept_disabled", hubAutoAcceptOff)
	case MaxModeFromSettings(ss)() < tunnel.TierLaunch:
		refuseAutoAccept(w, http.StatusForbidden, "hub_mode_too_low", "An agent that skips permission prompts needs Coral Hub access set to Launch on this computer")
	default:
		return false
	}
	return true
}
