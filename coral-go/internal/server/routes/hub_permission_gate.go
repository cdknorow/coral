// What a Coral Hub request may ask an agent to skip (#126 F4).

package routes

import (
	"context"
	"net/http"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// hubAutoAcceptOff is the hub_auto_accept_disabled refusal: what is off, and where on this computer
// the owner turns it on.
const hubAutoAcceptOff = `Coral Hub auto-accept is turned off on this computer. To turn it on, open Settings > Network and Hub on this computer and turn on "Coral Hub can turn on auto-accept" (the account this computer is linked to can also turn it on from Coral Hub).`

// hubAsksToSkip reports whether a launch, team member or restart asks for an agent that acts
// without asking: flags that skip permission prompts (agent.SkipsPermissionPrompts, an allowlist),
// capability allow rules, allowed tools or custom hooks (the commands its CLI would run itself),
// the same choices limitAgentLaunch takes from an agent (#4032). Deny rules only narrow.
func hubAsksToSkip(agentType string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}) bool {
	return agent.SkipsPermissionPrompts(agentType, flags) || hubWidens(caps, tools, hooks)
}

// autoDefaultAllowed is whether a new launch that names no permission mode may start Claude in Auto (#5836): always for a request from this computer; through Coral Hub only when the
// owner allows an agent to skip permission prompts (the switch on, Launch mode, dashboard provenance: the rule an explicit --permission-mode auto meets), else the agent asks
// ("default"): a plain Hub launch still works with the switch off, it is just not started in a mode the owner did not allow. A settings read that fails is "no" (fail closed).
func autoDefaultAllowed(ctx context.Context, r *http.Request, ss *store.SessionStore) bool {
	_, _, refused, err := hubSkipRefusal(ctx, r, ss, true)
	return err == nil && !refused
}

// hubWidens is hubAsksToSkip's part that is not flags: capability allow rules, allowed tools or custom hooks.
func hubWidens(caps *agent.Capabilities, tools []string, hooks map[string]interface{}) bool {
	return caps != nil && len(caps.Allow) > 0 || len(tools) > 0 || len(hooks) > 0
}

// hubSkipRefusal is the decision of refuseHubSkip without the answer: for a request that carries the Hub's stamp and asks to skip permission
// prompts (skips), refused is true with the refusal's code and message when the owner does not allow it; refused is false (code and msg empty) when
// the request is allowed or is not gated at all (no stamp, or it does not skip). The caller reads the settings again when the same request is checked
// more than once (a team launch checks each member that skips, #5839); err is a failed settings read, never a decision.
func hubSkipRefusal(ctx context.Context, r *http.Request, ss *store.SessionStore, skips bool) (code, msg string, refused bool, err error) {
	if !skips || !cloud.Tunneled(r) {
		return "", "", false, nil
	}
	settings, err := ss.GetSettings(ctx)
	if err != nil {
		return "", "", false, err
	}
	switch {
	case !cloud.TunneledAuthentic(r):
		return "no_dashboard_provenance", "Auto-accept can only be turned on from the Coral dashboard or the Coral Hub", true, nil
	case !HubAutoAcceptAllowed(settings):
		return "hub_auto_accept_disabled", hubAutoAcceptOff, true, nil
	case MaxModeFromSettings(ss)() < tunnel.TierLaunch:
		return "hub_mode_too_low", "An agent that skips permission prompts needs Coral Hub access set to Launch on this computer", true, nil
	}
	return "", "", false, nil
}

// refuseHubSkip answers a Coral Hub request that asks to skip permission prompts (skips) unless the
// owner allowed that on this computer, and reports whether it refused: the agent auto-accept rule
// (#3182, #3377 SEC-JB-01), on every route that starts an agent with the request's choices (launch,
// launch-team, restart, tasks/run). The request must carry the Hub's provenance, the local switch
// "Coral Hub can turn on auto-accept" must be on (off unless the owner turned it on) and the Launch
// mode must be set. It is refused, never stripped, so the person sees why. A local request is not
// gated: the owner chooses at the dashboard, the command line or with the API key.
func refuseHubSkip(w http.ResponseWriter, r *http.Request, ss *store.SessionStore, skips bool) bool {
	code, msg, refused, err := hubSkipRefusal(r.Context(), r, ss, skips)
	if err != nil {
		errInternalServer(w, err.Error())
		return true
	}
	if refused {
		refuseAutoAccept(w, http.StatusForbidden, code, msg)
	}
	return refused
}
