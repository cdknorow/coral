package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// By the actor under enforce (#4255): the Operator the identity layer verified (the dashboard's cookie or
	// the signed command line); in log mode the earlier test (a person: humanInputCaller) stands, and where
	// the two differ (a browser with no sign-in, on an install that opted out) it is counted.
	if subscriber == crossTeamOperatorID {
		legacy := humanInputCaller(r)
		byActor := callerActor(r).Kind == ActorOperator
		allowed := legacy
		if identityActorEnforced() {
			allowed = byActor
		} else if legacy != byActor {
			noteEnforceDiffers("relay-operator", r)
		}
		if !allowed {
			relayError(w, http.StatusForbidden, "operator_requires_person", "only the Operator in the dashboard or the signed coral command line can message another Coral as the Operator", 0)
			return "", false
		}
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
	role, _, refusal, err := h.checkCrossTeamSender(ctx, project, subscriber, sessionName)
	switch {
	case err != nil:
		errInternalServer(w, err.Error())
		return "", false
	case refusal == senderNoBoard:
		relayError(w, http.StatusNotFound, "not_found", fmt.Sprintf("board %q not found", project), 0)
		return "", false
	case refusal == senderNotOrchestrator:
		relayError(w, http.StatusForbidden, "not_orchestrator", fmt.Sprintf("only an active Orchestrator of %q or the Operator can message another Coral", project), 0)
		return "", false
	case refusal == senderSessionMismatch:
		relayError(w, http.StatusForbidden, "session_mismatch", fmt.Sprintf("session_name does not match %q's subscription on %q; send from that agent's own session", subscriber, project), 0)
		return "", false
	}
	return role, true
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
	var body relayRequest
	if err := decodeJSON(r, &body); err != nil {
		relayError(w, http.StatusBadRequest, "bad_request", "invalid JSON", 0)
		return
	}
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if identityActorEnforced() { // an agent sends from its own session: the token's, not the one it names
		if a := callerActor(r); a.Kind == ActorToken {
			body.SessionName = a.Session
		}
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.ToAddress = strings.TrimSpace(body.ToAddress)
	if refused := body.refusal(); refused != nil {
		refused(w)
		return
	}
	ctx := r.Context()
	role, ok := h.relaySenderRole(w, r, project, body.SubscriberID, body.SessionName)
	if !ok {
		return
	}
	// The text goes to another computer and Coral Hub does not redact it: the same scan as an attachment's
	// (with the re-encoded views), before anything is sent or counted (#4449). Local posts and messages
	// received from another Coral are not scanned here.
	found, why := scanMessageTextWhy([]byte(body.Content))
	switch found {
	case scanFound:
		relayError(w, http.StatusUnprocessableEntity, "message_secret", "the message looks like it contains a secret (a token or a key)"+why.where()+", so it was not sent; remove it and send again", 0)
		return
	case scanUnchecked:
		relayError(w, http.StatusUnprocessableEntity, "message_unchecked", "the message could not be checked for secrets in time, so it was not sent; send a shorter message", 0)
		return
	}
	hops, refused := h.relayHops(ctx, project, body.ReplyTo)
	if refused != nil {
		refused(w)
		return
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
	prepared, ok := h.relayAttachments(w, r, project, &body)
	if !ok {
		return
	}
	// The files the text names by link go too, when the switch is on and they pass the same checks and limits (#5501).
	prepared = h.autoAttachLinked(ctx, r, project, &body, prepared)
	ownCrossTeamID, _ := h.senderIDOf(ctx, project) // this Coral's own board row's id (#5508), never taken from the request
	payload, err := json.Marshal(map[string]any{
		"message_id": messageID,
		"hops":       hops,
		"to_address": body.ToAddress,
		"body":       relayInnerBody(project, ownCrossTeamID, &body),
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	ans, ok := h.sendRelay(w, ctx, relay, project, &body, prepared, messageID, payload)
	if !ok {
		return
	}
	h.relayDelivered(w, ctx, project, &body, role, hops, messageID, ans, prepared)
}

// relayRequest is a PostRelayMessage request.
type relayRequest struct {
	SubscriberID string `json:"subscriber_id"`
	SessionName  string `json:"session_name"`
	Content      string `json:"content"`
	ToAddress    string `json:"to_address"`
	ReplyTo      int64  `json:"reply_to"`
	MessageID    string `json:"message_id"`
	// Attach are coral://artifacts links sent as files with the message (cross-team artifacts, C2).
	Attach []string `json:"attach"`
}

// refusal refuses a request with no sender or text, text over the limit, an address that isn't <server>/<team>,
// or a malformed message_id.
func (b *relayRequest) refusal() answer {
	refuse := func(status int, code, msg string) answer {
		return func(w http.ResponseWriter) { relayError(w, status, code, msg, 0) }
	}
	switch {
	case b.Content == "" || b.SubscriberID == "":
		return refuse(http.StatusBadRequest, "bad_request", "subscriber_id and content are required")
	case len(b.Content) > maxRelayedContent:
		return refuse(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content must be at most %d bytes", maxRelayedContent))
	case strings.Count(b.ToAddress, "/") >= 2:
		return refuse(http.StatusBadRequest, "bad_address", "an address is <server>/<team>; addressing an agent isn't supported")
	case !relayAddressRe.MatchString(b.ToAddress):
		return refuse(http.StatusBadRequest, "bad_address", "an address is <server>/<team>, e.g. homelab/qa (see coral-board targets)")
	case b.MessageID != "" && !relayIDRe.MatchString(b.MessageID):
		return refuse(http.StatusBadRequest, "bad_request", "malformed message_id")
	}
	return nil
}

// relayHops is the message's hop count: a reply to a message relayed from another Coral counts one more hop
// than it (refused past maxRelayHops, or when it doesn't reply to such a message).
func (h *BoardHandler) relayHops(ctx context.Context, project string, replyTo int64) (int, answer) {
	if replyTo <= 0 {
		return 0, nil
	}
	o, ok, err := h.bs.RelayedOrigin(ctx, project, replyTo)
	if err != nil {
		return 0, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	}
	if !ok {
		return 0, func(w http.ResponseWriter) {
			relayError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("message %d on %q is not a message from another Coral", replyTo, project), 0)
		}
	}
	hops := o.Hops + 1
	if hops > maxRelayHops {
		return 0, func(w http.ResponseWriter) {
			relayError(w, http.StatusBadRequest, "too_many_hops", fmt.Sprintf("a relayed reply chain is limited to %d hops", maxRelayHops), 0)
		}
	}
	return hops, nil
}

// relayAttachments checks and holds the message's files before anything is sent (a refusal here sends nothing),
// and appends their links to its text.
func (h *BoardHandler) relayAttachments(w http.ResponseWriter, r *http.Request, project string, b *relayRequest) (*attachPrepared, bool) {
	if len(b.Attach) == 0 {
		return nil, true
	}
	prepared, ok := h.prepareRelayAttachments(w, r, project, b.SubscriberID, b.SessionName, b.Attach)
	if !ok {
		return nil, false
	}
	b.Content += attachmentLines(prepared.atts)
	if len(b.Content) > maxRelayedContent {
		relayError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content and the attachment links must be at most %d bytes", maxRelayedContent), 0)
		return nil, false
	}
	return prepared, true
}

// sendRelay sends the files, then the message, to Coral Hub, within the sender's quota, and returns Coral Hub's
// answer when it took the message.
func (h *BoardHandler) sendRelay(w http.ResponseWriter, ctx context.Context, relay RelaySender, project string, b *relayRequest, prepared *attachPrepared, messageID string, payload []byte) (hubAnswer, bool) {
	// The sender's quota counts only requests that reach Coral Hub: a send that couldn't
	// (not linked, or the relay not offered) gets its token back.
	quotaKey := "relay-out\x00" + project + "\x00" + b.SubscriberID + "\x00" + b.ToAddress
	if secs, limited := h.crossTeamWait(quotaKey); limited {
		relayError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("retry in %ds", secs), secs)
		return hubAnswer{}, false
	}
	if prepared != nil {
		if ok, refund := h.sendAttachments(w, ctx, prepared, messageID, b.ToAddress, project); !ok {
			if refund {
				h.crossTeam.refund(quotaKey)
			}
			return hubAnswer{}, false
		}
	}
	resp, err := relay(ctx, tunnel.RelayMessagePath, payload)
	if err != nil {
		if errors.Is(err, cloud.ErrNotLinked) || errors.Is(err, tunnel.ErrRelayUnsupported) {
			h.crossTeam.refund(quotaKey)
		}
		relayUnavailable(w, err)
		return hubAnswer{}, false
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
		return hubAnswer{}, false
	}
	return ans, true
}

// relayDelivered stores the sent message's copy on this board (a duplicate keeps the copy its first attempt
// stored) and its shared files, and answers.
func (h *BoardHandler) relayDelivered(w http.ResponseWriter, ctx context.Context, project string, b *relayRequest, role string, hops int, messageID string, ans hubAnswer, prepared *attachPrepared) {
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
	server, team, _ := strings.Cut(b.ToAddress, "/")
	mirror, err := h.bs.PostRelayMirror(ctx, board.MessageOrigin{
		ServerID: store.LocalServerID, Board: project, Sender: b.SubscriberID, SenderRole: role,
		TargetServerID: server, TargetBoard: team, TargetMessageID: target,
		RelayMessageID: messageID, Hops: hops,
	}, b.Content)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	out := map[string]any{"ok": true, "duplicate": ans.Duplicate, "message_id": messageID, "target_message_id": target, "mirror": mirror}
	// A coral://artifacts link in the text that was not attached is a file on THIS computer only: the other Coral cannot open it (#5499). The message is still sent; the reply names the links.
	if links := unattachedArtifactLinks(b.Content, prepared); len(links) > 0 {
		out["warnings"] = []string{"unattached_artifact_links"}
		out["unattached_links"] = links
	}
	if prepared != nil && len(prepared.atts) > 0 {
		// The shared rows only now: the hub has the files and the message.
		h.recordSharedAttachments(prepared.atts, project, server, team, b.SubscriberID, mirror.ID, time.Now().UTC().Format(time.RFC3339))
		var ids []string
		for _, a := range prepared.atts {
			ids = append(ids, "coral://artifacts/"+a.ID)
		}
		out["attached"] = ids
		if prepared.auto > 0 { // the last ones came from links in the text, not from --attach (#5501)
			out["auto_attached"] = ids[len(ids)-prepared.auto:]
		}
	}
	writeJSON(w, http.StatusOK, out)
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
	subscriber, attrOK := attributedTo(w, r, strings.TrimSpace(q.Get("subscriber_id")))
	if !attrOK {
		return
	}
	sessionName := q.Get("session_name")
	if identityActorEnforced() {
		if a := callerActor(r); a.Kind == ActorToken {
			sessionName = a.Session
		}
	}
	if _, ok := h.relaySenderRole(w, r, project, subscriber, sessionName); !ok {
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

// artifactLinkRe is a coral://artifacts link as an agent writes it: the lower-case SHA-256, not part of a longer hex run.
// The /api/artifacts/<hex> form (what an upload's "url" and a copied dashboard link look like) names the same file on this computer and means just as little elsewhere (#5500).
var artifactLinkRe = regexp.MustCompile(`(?:coral://artifacts/|/api/artifacts/)([0-9a-f]{64})(?:[^0-9a-fA-F]|$)`)

// maxUnattachedLinksNamed bounds the reply: a message with a hundred links names the first few.
const maxUnattachedLinksNamed = 10

// unattachedArtifactLinks are the coral://artifacts links in text that were not sent as attachments with it, in order, once each: such a link means nothing on another computer.
func unattachedArtifactLinks(text string, prepared *attachPrepared) []string {
	attached := map[string]bool{}
	if prepared != nil {
		for _, a := range prepared.atts {
			attached[a.ID] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range artifactLinkRe.FindAllStringSubmatch(text, -1) {
		id := m[1]
		if attached[id] || seen[id] {
			continue
		}
		seen[id] = true
		if len(out) < maxUnattachedLinksNamed {
			out = append(out, "coral://artifacts/"+id)
		}
	}
	return out
}

// relayInnerBody is the message's inner body for the other Coral: the sender, the text, and (#5508) the id of this Coral's own board, which the receiving board checks against its list.
func relayInnerBody(project, ownCrossTeamID string, b *relayRequest) map[string]string {
	out := map[string]string{
		"from_board": project, "subscriber_id": b.SubscriberID, "session_name": b.SessionName,
		"content": b.Content, "to": "",
	}
	if board.ValidCrossTeamID(ownCrossTeamID) {
		out["from_cross_team_id"] = ownCrossTeamID
	}
	return out
}
