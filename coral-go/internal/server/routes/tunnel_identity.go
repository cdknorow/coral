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
	"github.com/cdknorow/coral/pkg/tunnel"
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
// The Operator (the dashboard's own identity: task planning, cross-team posts,
// completing others' tasks) may be used in any identity field when operatorAllowed says
// so: by default, unless the owner turned on "Don't let Coral Hub act as the Operator"
// (#3404); otherwise 403 operator_identity_required. It is never the Operator without the
// Coral Hub user: no X-Coral-Hub-User is 403 hub_user_required, a malformed or repeated
// one 400, a name already on the board 409. Each use is recorded and logged with the
// Hub user, and the handler sees it (hubOperatorUser). A chat post (POST …/messages)
// is not checked here: PostMessage applies the same rules to its sender.
//
// Any other identity is refused (#3428): an actor field that names an existing member or
// role of the board is 409 member_identity (as a chat post's name is), and any other name is
// 403 hub_identity_required, because through the tunnel a board write is made as the Operator
// or not at all (a Coral Hub user cannot name itself: hub-user-* is reserved). A write that
// names no actor at all (pause, resume, a reminder) is the Operator's too: it needs
// Operator-by-default, so the opt-out covers every write, and it is recorded like the rest.
// POST/DELETE /reminder names the REMINDED subscriber in subscriber_id, which is a target and
// not an actor. Every board write through the tunnel is recorded with its Hub user.
//
// Requests from this computer are unchanged (#3318/#2969 is the plan for those).
func TunnelIdentityGuard(bs *board.Store, operatorAllowed func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			project, ok := tunnelBoardProject(r)
			// A cross-team message Coral Hub relayed from another Coral (#3489) is a
			// server's, not a Coral Hub user's: PostExternalMessage checks its source
			// server against the board's allowlist. Only an authentic relay skips this
			// guard; a forged X-Coral-Hub-Server still goes through it (and is refused).
			// Coral Hub's own notice about a team link (#3489) is the hub's, not a user's:
			// PostHubNotice accepts only that, with its fixed template.
			if !ok || bs == nil || isBoardChatPost(r) || isAuthenticRelay(r) || isAuthenticHubNotice(r) {
				next.ServeHTTP(w, r)
				return
			}
			values, receiveAll, err := tunnelIdentityValues(r)
			if err != nil {
				errBadRequest(w, "could not read the request body")
				return
			}
			if receiveAll {
				refuseOrchestratorIdentity("Through Coral Hub a subscriber can't receive every message (receive_mode all is the orchestrator's)")(w)
				return
			}
			allowed := func() bool { return operatorAllowed != nil && operatorAllowed(r) }
			asOperator, anyActor, refused := tunnelOrchestratorRules(r, bs, project, values, allowed)
			if refused != nil {
				refused(w)
				return
			}
			if refused := tunnelOtherIdentities(r, bs, project, values); refused != nil {
				refused(w)
				return
			}
			if !anyActor {
				// A write that names no actor is the Operator's (pause, resume, a reminder).
				if !allowed() {
					operatorIdentityRequired(w)
					return
				}
				asOperator = true
			}
			if asOperator {
				serveAsHubOperator(w, r, next, bs, project)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// tunnelIdentityValues collects every identity field's values, from the query and from a JSON body (whose bytes
// are put back for the handler), and whether the body asks for receive_mode "all".
func tunnelIdentityValues(r *http.Request) (map[string][]string, bool, error) {
	values := map[string][]string{}
	for _, f := range tunnelOperatorFields {
		values[f] = append(values[f], r.URL.Query()[f]...)
	}
	receiveAll := false
	if r.Body == nil || r.Body == http.NoBody {
		return values, false, nil
	}
	raw, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, false, err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return values, false, nil
	}
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
	return values, receiveAll, nil
}

// tunnelOrchestratorRules applies every orchestrator and Operator rule, over all the identity fields: the
// Operator when allowed (asOperator; anyActor when it is named as an actor, not only as an assignee), never an
// orchestrator by name or by an existing subscriber's rights.
func tunnelOrchestratorRules(r *http.Request, bs *board.Store, project string, values map[string][]string, allowed func() bool) (asOperator, anyActor bool, refused answer) {
	for _, f := range tunnelOperatorFields {
		for _, v := range values[f] {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if strings.EqualFold(v, operatorIdentity) {
				if !allowed() {
					return false, false, operatorIdentityRequired
				}
				asOperator = true
				anyActor = anyActor || f != "assignee" && f != "assigned_to"
				continue
			}
			if f == "assignee" || f == "assigned_to" {
				continue // who a task is for: anyone but the Operator rule above
			}
			if board.IsOrchestrator(v, "", 0) {
				return false, false, refuseOrchestratorIdentity("Through Coral Hub you can't act as an orchestrator (" + f + ")")
			}
			if f == "job_title" {
				continue
			}
			if sub, err := bs.GetProjectSubscription(r.Context(), project, v); err == nil && sub != nil && board.IsOrchestrator(sub.SubscriberID, sub.JobTitle, sub.CanPeek) {
				return false, false, refuseOrchestratorIdentity("Through Coral Hub you can't act as this board's orchestrator (" + f + ")")
			}
		}
	}
	return asOperator, anyActor, nil
}

// tunnelOtherIdentities refuses any other identity in an actor field: an existing member or role (409), or a
// made-up name (403). Through the tunnel no actor but the Operator passes; the reminded subscriber is a target,
// not an actor.
func tunnelOtherIdentities(r *http.Request, bs *board.Store, project string, values map[string][]string) answer {
	targetRoute := isBoardReminderWrite(r)
	var subs []board.Subscriber
	if got, err := bs.ListSubscribers(r.Context(), project); err == nil {
		subs = got
	}
	namesMemberOrRole := func(v string) bool {
		for _, sub := range subs {
			if board.SameIdentity(sub.SubscriberID, v) || board.SameIdentity(strings.TrimSpace(sub.JobTitle), v) {
				return true
			}
		}
		return false
	}
	for _, f := range tunnelActorFields {
		for _, v := range values[f] {
			v = strings.TrimSpace(v)
			if v == "" || strings.EqualFold(v, operatorIdentity) {
				continue
			}
			if f == "subscriber_id" && targetRoute {
				continue // the reminded subscriber: a target, not an actor
			}
			if namesMemberOrRole(v) {
				slog.Warn("[cloud] Coral Hub write refused: it named an existing member or role", "field", f, "method", r.Method, "path", r.URL.Path)
				return func(w http.ResponseWriter) {
					writeJSON(w, http.StatusConflict, map[string]any{"code": "member_identity",
						"error": "That name is already a member or role on this board: through Coral Hub a write can't be made as an existing member (" + f + ")"})
				}
			}
			return func(w http.ResponseWriter) {
				writeJSON(w, http.StatusForbidden, map[string]any{"code": "hub_identity_required",
					"error": "Through Coral Hub a board write is made as the Operator (or a chat post as the Coral Hub user); this name is neither (" + f + ")"})
			}
		}
	}
	return nil
}

// serveAsHubOperator serves a write made as the Operator, with its Coral Hub user (refused without a valid one),
// and records it with the status the handler answered.
func serveAsHubOperator(w http.ResponseWriter, r *http.Request, next http.Handler, bs *board.Store, project string) {
	user, present, status, msg := tunnelHubUser(r, bs, project)
	if !present {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "hub_user_required",
			"error": "Acting as the Operator through Coral Hub needs the Coral Hub user (" + tunnel.HubUserHeader + "); this hub does not send it"})
		return
	}
	if status != 0 {
		writeJSON(w, status, map[string]any{"error": msg, "code": "hub_user_required"})
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	next.ServeHTTP(rec, withHubOperator(r, user))
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	recordHubOperatorAction(r.Context(), bs, user, r.Method, r.URL.Path, project, rec.status)
}

func refuseOrchestratorIdentity(msg string) answer {
	return func(w http.ResponseWriter) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": msg, "code": "orchestrator_identity"})
	}
}

func operatorIdentityRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required",
		"error": "Through Coral Hub you can't act as the Operator: 'Don't let Coral Hub act as the Operator' is on in Settings on this computer"})
}

// isBoardReminderWrite is POST or DELETE /api/board/{project}/reminder: its subscriber_id
// names the subscriber being reminded.
func isBoardReminderWrite(r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		return false
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/board/")
	_, tail, _ := strings.Cut(rest, "/")
	return tail == "reminder"
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
