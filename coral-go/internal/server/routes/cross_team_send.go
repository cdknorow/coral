package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// Sending a cross-team message to another Coral of the same account through Coral Hub
// (#3489 part 2, coral-hub PR #260). The Orchestrator (or the Operator) of a board here
// sends to an ADDRESS, "<server>/<team>", that the account owner linked this board to in
// Coral Hub; the hub resolves it and delivers to that board's Orchestrator. Coral only
// checks its own side (the sender, the board's opt-in, the address syntax); whether the
// link exists is the hub's to say (a uniform 404 otherwise). Both routes are for this
// computer only: they are not in the tunnel's route table, so Coral Hub can't call them.

// RelaySender sends one relay request to Coral Hub (cloud.Manager.Relay).
type RelaySender func(ctx context.Context, path string, body []byte) (tunnel.RelayResponse, error)

// SetRelay attaches the Coral Hub relay (nil: relayed sends answer 503 not_linked).
func (h *BoardHandler) SetRelay(fn RelaySender) {
	h.mu.Lock()
	h.relay = fn
	h.mu.Unlock()
}

func (h *BoardHandler) relaySender() RelaySender {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.relay
}

// relayAddressRe is "<server>/<team>": a server slug (or id-<12 hex>) and a board name.
// A third part (an agent) is reserved and refused in v1.
var (
	relayAddressRe = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,31}|id-[0-9a-f]{12})/([A-Za-z0-9][A-Za-z0-9._-]{0,63})$`)
	relayTeamRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// relayError answers a relayed send's refusal with a code (and Retry-After when set).
func relayError(w http.ResponseWriter, status int, code, msg string, retryAfter int) {
	body := map[string]any{"ok": false, "error": code, "message": msg}
	if status == http.StatusTooManyRequests {
		httputil.WriteActionLimitBody(w, retryAfter, body)
		return
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	writeJSON(w, status, body)
}

// relaySenderRole checks the sender of a relayed send or lookup on board: the Operator,
// or an active Orchestrator of it calling from its own session (the same rules as a
// local cross-team message), and the board's cross-team opt-in. The Operator must be a
// person here (the dashboard or the operator's signed command line): a message that
// leaves this computer can't be sent as the Operator by an anonymous local process
// (Orchestrator's decision; the local cross-team route is unchanged). It writes the
// refusal and returns ok false.
func (h *BoardHandler) relaySenderRole(w http.ResponseWriter, r *http.Request, project, subscriber, sessionName string) (string, bool) {
	ctx := r.Context()
	if !relayTeamRe.MatchString(project) {
		relayError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("board %q can't be used in an address (letters, digits, '.', '_', '-'; up to 64)", project), 0)
		return "", false
	}
	if subscriber == crossTeamOperatorID && !humanInputCaller(r) {
		relayError(w, http.StatusForbidden, "operator_requires_person", "only the Operator in the dashboard or the signed coral command line can message another Coral as the Operator", 0)
		return "", false
	}
	role, ok := h.relaySenderIdentity(w, r, project, subscriber, sessionName)
	if !ok {
		return "", false
	}
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil {
		errInternalServer(w, err.Error())
		return "", false
	}
	if !settings.CrossTeamMessaging {
		relayError(w, http.StatusForbidden, "cross_team_disabled", fmt.Sprintf("cross-team messaging is disabled on board %q; the Operator can enable it in Team settings", project), 0)
		return "", false
	}
	return role, true
}

// relaySenderIdentity checks who the sender is (the Operator, or an active Orchestrator from its
// own session).
func (h *BoardHandler) relaySenderIdentity(w http.ResponseWriter, r *http.Request, project, subscriber, sessionName string) (string, bool) {
	ctx := r.Context()
	if subscriber == crossTeamOperatorID {
		if ok, err := h.bs.BoardExists(ctx, project); err != nil {
			errInternalServer(w, err.Error())
			return "", false
		} else if !ok {
			relayError(w, http.StatusNotFound, "not_found", fmt.Sprintf("board %q not found", project), 0)
			return "", false
		}
		return crossTeamOperatorID, true
	}
	sub, err := h.bs.GetProjectSubscription(ctx, project, subscriber)
	if err != nil {
		errInternalServer(w, err.Error())
		return "", false
	}
	if sub == nil || sub.IsActive == 0 || !strings.EqualFold(strings.TrimSpace(sub.JobTitle), crossTeamSenderRole) {
		relayError(w, http.StatusForbidden, "not_orchestrator", fmt.Sprintf("only an active Orchestrator of %q or the Operator can message another Coral", project), 0)
		return "", false
	}
	if sn := strings.TrimSpace(sessionName); sn == "" || sub.SessionName == "" || sn != sub.SessionName {
		relayError(w, http.StatusForbidden, "session_mismatch", fmt.Sprintf("session_name does not match %q's subscription on %q; send from that agent's own session", subscriber, project), 0)
		return "", false
	}
	return crossTeamSenderRole, true
}

// relayUnavailable maps a failure to reach Coral Hub to an answer.
func relayUnavailable(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cloud.ErrNotLinked):
		relayError(w, http.StatusServiceUnavailable, "not_linked", "this Coral isn't connected to Coral Hub", 0)
	case errors.Is(err, tunnel.ErrRelayUnsupported):
		relayError(w, http.StatusNotImplemented, "relay_unsupported", "Coral Hub doesn't relay messages (its relay is switched off, or the hub is too old)", 0)
	default:
		relayError(w, http.StatusBadGateway, "hub_unreachable", "couldn't reach Coral Hub; retry with the same message_id", 0)
	}
}

// hubAnswer is the part of Coral Hub's relay answer Coral reads (anything else is dropped).
type hubAnswer struct {
	OK              bool            `json:"ok"`
	Error           string          `json:"error"`
	Reason          string          `json:"reason"`
	Duplicate       bool            `json:"duplicate"`
	TargetMessageID json.RawMessage `json:"target_message_id"`
}

// hubErrorCode keeps a hub error code only if it is a plain code (never free text).
var hubErrorCodeRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

// PostRelayMessage sends a cross-team message to another Coral through Coral Hub.
// POST /api/board/{project}/relay-messages (this computer only)
// {"subscriber_id", "session_name", "content", "to_address": "<server>/<team>", "reply_to": <message id>?, "message_id": "<to retry>"?}
func (h *BoardHandler) PostRelayMessage(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		relayError(w, http.StatusForbidden, "local_only", "messages to another Coral are sent from this computer only", 0)
		return
	}
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionName  string `json:"session_name"`
		Content      string `json:"content"`
		ToAddress    string `json:"to_address"`
		ReplyTo      int64  `json:"reply_to"`
		MessageID    string `json:"message_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		relayError(w, http.StatusBadRequest, "bad_request", "invalid JSON", 0)
		return
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.ToAddress = strings.TrimSpace(body.ToAddress)
	switch {
	case body.Content == "" || body.SubscriberID == "":
		relayError(w, http.StatusBadRequest, "bad_request", "subscriber_id and content are required", 0)
		return
	case len(body.Content) > maxRelayedContent:
		relayError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content must be at most %d bytes", maxRelayedContent), 0)
		return
	case strings.Count(body.ToAddress, "/") >= 2:
		relayError(w, http.StatusBadRequest, "bad_address", "an address is <server>/<team>; addressing an agent isn't supported", 0)
		return
	case !relayAddressRe.MatchString(body.ToAddress):
		relayError(w, http.StatusBadRequest, "bad_address", "an address is <server>/<team>, e.g. homelab/qa (see coral-board targets)", 0)
		return
	case body.MessageID != "" && !relayIDRe.MatchString(body.MessageID):
		relayError(w, http.StatusBadRequest, "bad_request", "malformed message_id", 0)
		return
	}
	ctx := r.Context()
	role, ok := h.relaySenderRole(w, r, project, body.SubscriberID, body.SessionName)
	if !ok {
		return
	}
	// A reply to a message relayed from another Coral counts one more hop.
	hops := 0
	if body.ReplyTo > 0 {
		o, ok, err := h.bs.RelayedOrigin(ctx, project, body.ReplyTo)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if !ok {
			relayError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("message %d on %q is not a message from another Coral", body.ReplyTo, project), 0)
			return
		}
		hops = o.Hops + 1
		if hops > maxRelayHops {
			relayError(w, http.StatusBadRequest, "too_many_hops", fmt.Sprintf("a relayed reply chain is limited to %d hops", maxRelayHops), 0)
			return
		}
	}
	relay := h.relaySender()
	if relay == nil {
		relayUnavailable(w, cloud.ErrNotLinked)
		return
	}
	messageID := body.MessageID
	if messageID == "" {
		messageID = uuid.NewString()
	}
	payload, err := json.Marshal(map[string]any{
		"message_id": messageID,
		"hops":       hops,
		"to_address": body.ToAddress,
		"body": map[string]string{
			"from_board": project, "subscriber_id": body.SubscriberID, "session_name": body.SessionName,
			"content": body.Content, "to": "",
		},
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// The sender's quota counts only requests that reach Coral Hub: a send that couldn't
	// (not linked, or the relay not offered) gets its token back.
	quotaKey := "relay-out\x00" + project + "\x00" + body.SubscriberID + "\x00" + body.ToAddress
	if allowed, wait := h.crossTeam.allow(quotaKey); !allowed {
		relayError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("retry in %ds", int(math.Ceil(wait.Seconds()))), int(math.Ceil(wait.Seconds())))
		return
	}
	resp, err := relay(ctx, tunnel.RelayMessagePath, payload)
	if err != nil {
		if errors.Is(err, cloud.ErrNotLinked) || errors.Is(err, tunnel.ErrRelayUnsupported) {
			h.crossTeam.refund(quotaKey)
		}
		relayUnavailable(w, err)
		return
	}
	var ans hubAnswer
	_ = json.Unmarshal(resp.Body, &ans)
	if resp.Status != http.StatusOK || !ans.OK {
		code := ans.Error
		if !hubErrorCodeRe.MatchString(code) {
			code = "hub_error"
		}
		if ans.Reason != "" && hubErrorCodeRe.MatchString(ans.Reason) {
			code = code + ":" + ans.Reason
		}
		status := resp.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		relayError(w, status, code, "Coral Hub refused the message; retry with the same message_id if it is temporary", resp.RetryAfter)
		return
	}
	var target int64
	if n, err := strconv.ParseInt(strings.Trim(string(ans.TargetMessageID), `"`), 10, 64); err == nil {
		target = n
	}
	// A retry Coral Hub answers "duplicate" was delivered before: keep the copy stored then
	// (one is stored now only if that first attempt never got to store it).
	if ans.Duplicate {
		if id, err := h.bs.RelayMirrorID(ctx, project, messageID); err != nil {
			errInternalServer(w, err.Error())
			return
		} else if id > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true, "message_id": messageID, "target_message_id": target, "mirror_id": id})
			return
		}
	}
	server, team, _ := strings.Cut(body.ToAddress, "/")
	mirror, err := h.bs.PostRelayMirror(ctx, board.MessageOrigin{
		ServerID: store.LocalServerID, Board: project, Sender: body.SubscriberID, SenderRole: role,
		TargetServerID: server, TargetBoard: team, TargetMessageID: target,
		RelayMessageID: messageID, Hops: hops,
	}, body.Content)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": ans.Duplicate, "message_id": messageID, "target_message_id": target, "mirror": mirror})
}

// relayTarget is one address Coral Hub says this board may message.
type relayTarget struct {
	Address   string `json:"address"`
	Server    string `json:"server"`
	Team      string `json:"team"`
	Reachable bool   `json:"reachable"`
}

// ListRelayTargets asks Coral Hub which other Corals' teams this board may message.
// GET /api/board/{project}/relay-targets?subscriber_id=...&session_name=... (this computer only)
func (h *BoardHandler) ListRelayTargets(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		relayError(w, http.StatusForbidden, "local_only", "the relay targets are listed from this computer only", 0)
		return
	}
	project := urlParam(r, "project")
	q := r.URL.Query()
	if _, ok := h.relaySenderRole(w, r, project, strings.TrimSpace(q.Get("subscriber_id")), q.Get("session_name")); !ok {
		return
	}
	relay := h.relaySender()
	if relay == nil {
		relayUnavailable(w, cloud.ErrNotLinked)
		return
	}
	payload, _ := json.Marshal(map[string]string{"from_board": project})
	resp, err := relay(r.Context(), tunnel.RelayTargetsPath, payload)
	if err != nil {
		relayUnavailable(w, err)
		return
	}
	var ans struct {
		OK      bool          `json:"ok"`
		Error   string        `json:"error"`
		Targets []relayTarget `json:"targets"`
	}
	if json.Unmarshal(resp.Body, &ans) != nil || resp.Status != http.StatusOK || !ans.OK {
		code := ans.Error
		if !hubErrorCodeRe.MatchString(code) {
			code = "hub_error"
		}
		status := resp.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		relayError(w, status, code, "Coral Hub didn't list the targets", resp.RetryAfter)
		return
	}
	// Only well-formed entries, and at most the hub's cap.
	out := []relayTarget{}
	for _, t := range ans.Targets {
		if len(out) == 50 {
			break
		}
		if relayAddressRe.MatchString(t.Address) && t.Address == t.Server+"/"+t.Team {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "targets": out})
}
