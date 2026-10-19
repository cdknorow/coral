package routes

import (
	"fmt"
	"net/http"

	"github.com/cdknorow/coral/internal/httputil"
	setreg "github.com/cdknorow/coral/internal/settings"
)

// The ceiling on the live agents an agent token may add (#4602): POST /api/sessions/launch and /launch-team are routes an agent
// token can call (an Orchestrator starts its team), and nothing but the machine bounded a loop that kept calling them. A launch by
// an agent token is refused with 429 agent_limit once Coral already has AgentLaunchLimitSetting live agents (running, not asleep),
// or would pass it with the agents a launch-team names. The Operator, the dashboard and the command line are never refused: the
// person decides how many agents their machine runs. 0 turns the ceiling off.
const AgentLaunchLimitSetting = setreg.AgentLaunchLimit

// agentLaunchLimit is the ceiling in force: the setting, or the default when it is unset or unreadable.
func (h *SessionsHandler) agentLaunchLimit(r *http.Request) int {
	settings, err := h.ss.GetSettings(r.Context())
	if err != nil {
		return int(setreg.Int(nil, AgentLaunchLimitSetting))
	}
	return int(setreg.Int(settings, AgentLaunchLimitSetting))
}

// refuseAgentLaunchOverLimit answers 429 agent_limit, and reports true, when the caller is an agent token and adding more live
// agents would pass the ceiling. A failed count is not a refusal: the ceiling never turns a working launch into an error.
func (h *SessionsHandler) refuseAgentLaunchOverLimit(w http.ResponseWriter, r *http.Request, adding int) bool {
	if callerActor(r).Kind != ActorToken {
		return false
	}
	limit := h.agentLaunchLimit(r)
	if limit == 0 {
		return false
	}
	live, err := h.ss.CountActiveLiveSessions(r.Context())
	if err != nil || live+adding <= limit {
		return false
	}
	msg := fmt.Sprintf("Coral has %d live agents and an agent may not start more past %d (this launch adds %d): stop some agents, or the Operator can raise the %s setting (PUT /api/settings; 0 for no ceiling)", live, limit, adding, AgentLaunchLimitSetting)
	httputil.WriteActionLimitBody(w, 0, map[string]any{"error": msg, "message": msg, "code": "agent_limit", "live": live, "max": limit, "not_started": true})
	return true
}
