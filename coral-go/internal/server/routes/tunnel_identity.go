package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
)

// tunnelActorFields name who is acting in a board write. (assignee / assigned_to name
// who a task is for, which anyone may choose; but not the Operator, see below.)
var tunnelActorFields = []string{"subscriber_id", "session_id", "as", "created_by", "job_title"}

// tunnelOperatorFields are every identity field checked for the Operator.
var tunnelOperatorFields = append(append([]string(nil), tunnelActorFields...), "assignee", "assigned_to")

const operatorIdentity = "Operator"

// TunnelIdentityGuard keeps a Coral Hub user (through the tunnel, at any tier) from
// acting with an orchestrator's rights on a board (#3377, SEC-BD-02): every message,
// task planning, peeking. A board write through the tunnel is refused (403
// orchestrator_identity) when an actor field contains "orchestrator", names an existing
// subscriber with orchestrator or peek rights, or the body asks for receive_mode "all".
//
// The Operator (the dashboard's own identity: task planning, cross-team posts) is
// refused the same way, 403 operator_identity_required, in any identity field, unless
// operatorAllowed says the owner turned on "Let Coral Hub act as the Operator"; each
// such use is logged. A chat post (POST …/messages) is not checked here: its sender is
// the Coral Hub user from X-Coral-Hub-User (PostMessage).
//
// Requests from this computer are unchanged (#3318/#2969 is the plan for those).
func TunnelIdentityGuard(bs *board.Store, operatorAllowed func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			project, ok := tunnelBoardProject(r)
			if !ok || bs == nil || isBoardChatPost(r) {
				next.ServeHTTP(w, r)
				return
			}
			values := map[string][]string{}
			for _, f := range tunnelOperatorFields {
				values[f] = append(values[f], r.URL.Query()[f]...)
			}
			receiveAll := false
			if r.Body != nil && r.Body != http.NoBody {
				raw, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					errBadRequest(w, "could not read the request body")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) == nil {
					for _, f := range tunnelOperatorFields {
						var s string
						if v, ok := body[f]; ok && json.Unmarshal(v, &s) == nil {
							values[f] = append(values[f], s)
						}
					}
					var mode string
					if v, ok := body["receive_mode"]; ok && json.Unmarshal(v, &mode) == nil {
						receiveAll = strings.EqualFold(strings.TrimSpace(mode), "all")
					}
				}
			}
			refuse := func(msg string) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": msg, "code": "orchestrator_identity"})
			}
			if receiveAll {
				refuse("Through Coral Hub a subscriber can't receive every message (receive_mode all is the orchestrator's)")
				return
			}
			for f, vs := range values {
				for _, v := range vs {
					v = strings.TrimSpace(v)
					if v == "" {
						continue
					}
					if strings.EqualFold(v, operatorIdentity) {
						if operatorAllowed == nil || !operatorAllowed(r) {
							writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required",
								"error": "Through Coral Hub you can't act as the Operator unless 'Let Coral Hub act as the Operator' is on in Settings on this computer"})
							return
						}
						slog.Info("[cloud] Coral Hub acted as the Operator", "method", r.Method, "path", r.URL.Path, "field", f)
						continue
					}
					if f == "assignee" || f == "assigned_to" {
						continue // who a task is for: anyone but the Operator rule above
					}
					if board.IsOrchestrator(v, "", 0) {
						refuse("Through Coral Hub you can't act as an orchestrator (" + f + ")")
						return
					}
					if f == "job_title" {
						continue
					}
					if sub, err := bs.GetProjectSubscription(r.Context(), project, v); err == nil && sub != nil && board.IsOrchestrator(sub.SubscriberID, sub.JobTitle, sub.CanPeek) {
						refuse("Through Coral Hub you can't act as this board's orchestrator (" + f + ")")
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isBoardChatPost is POST /api/board/{project}/messages.
func isBoardChatPost(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/board/")
	_, tail, _ := strings.Cut(rest, "/")
	return tail == "messages"
}

// tunnelBoardProject returns the project of a board write that came through the
// tunnel; ok is false for anything else.
func tunnelBoardProject(r *http.Request) (string, bool) {
	if !cloud.Tunneled(r) || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return "", false
	}
	rest, found := strings.CutPrefix(r.URL.Path, "/api/board/")
	if !found {
		return "", false
	}
	project, _, _ := strings.Cut(rest, "/")
	switch project {
	case "", "projects", "tasks", "remotes":
		return "", false
	}
	return project, true
}
