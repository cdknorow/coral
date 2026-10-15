package routes

import "net/http"

// callerSession binds a request on a route an agent calls to the caller's session
// (#3495, Security's sweep #3493; the rule of #3481 part 1 for the events route):
//   - with a verified session token, the token decides the session: a claimed
//     session_id naming another one is 403 identity_mismatch, and an empty one is
//     the token's;
//   - without one, only the Operator (the dashboard, the verified command line, an
//     authentic Coral Hub request) is accepted, on routes the dashboard uses
//     (operatorOK); anything else is 401 session_token_required, in every mode,
//     with one exception: tokenless.
//
// tokenless, when not nil, reports whether the claimed session can't hold a token.
// A terminal session is a plain shell Coral starts with no launch command, so it is
// never given one, and coral-agent run in it finds its session through tmux. Its
// token-less requests are accepted in default mode (the operator's rule: bare scripts
// and the command line work there without a credential). Strict local auth refuses
// them before they get here (IdentityObserver.Middleware), and every agent session
// (claude, codex, agy, pi) needs its own token in every mode.
//
// Coral's agent tools (coral-agent, coral-board, the hooks) always send the token.
func callerSession(w http.ResponseWriter, r *http.Request, claimed string, operatorOK bool, tokenless func(sessionID string) bool) (string, bool) {
	if owner, ok := VerifiedSession(r.Context()); ok {
		if claimed != "" && claimed != owner.SessionID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_mismatch",
				"message": "An agent can act only for its own session"})
			return "", false
		}
		return owner.SessionID, true
	}
	if operatorOK && humanInputCaller(r) {
		return claimed, true
	}
	if tokenless != nil && claimed != "" && tokenless(claimed) {
		return claimed, true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_token_required",
		"message": "This comes from the agent's own Coral tools, with its session token. Restart the agent from Coral if it predates session tokens."})
	return "", false
}
