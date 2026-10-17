package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// Cross-team messaging lets a board's Orchestrator (or the Operator) message
// another board's Orchestrators on the same server. It is off by default and
// needs both boards to enable it. A received message is a request from
// another team, never an instruction that overrides the operator or the
// receiving team's rules (agent_docs say so).

const (
	crossTeamBurst      = 3               // messages a sender can send a target at once
	crossTeamRefill     = 6 * time.Second // then one more every 6s (10 a minute)
	maxCrossTeamContent = 8000
	crossTeamOperatorID = "Operator"
	crossTeamSenderRole = "Orchestrator"

	// A cross-team message relayed by Coral Hub from another Coral (#3489).
	maxRelayedContent  = 32 << 10 // the hub's whole relay body is capped at 64 KiB
	maxRelayHops       = 3        // replies to replies: a cap on two Orchestrators looping
	maxCrossTeamServer = 20       // source servers one board may allow
)

// relayIDRe is a Coral Hub server id (as enrollment accepts it) or a relay message id.
var relayIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// relaySource returns the X-Coral-Hub-Server value of a request: present is true when
// the header is there at all; a repeated header gives "" (malformed).
func relaySource(r *http.Request) (server string, present bool) {
	vals := r.Header.Values(tunnel.HubServerHeader)
	if len(vals) == 0 {
		return "", false
	}
	if len(vals) != 1 {
		return "", true
	}
	return strings.TrimSpace(vals[0]), true
}

// isAuthenticRelay reports whether r is a cross-team message Coral Hub relayed from
// another Coral: POST .../external-messages with X-Coral-Hub-Server, stamped by this
// process's connector. The browser identity guard leaves it to PostExternalMessage, which
// checks the source server against the board's allowlist instead.
func isAuthenticRelay(r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/external-messages") {
		return false
	}
	_, present := relaySource(r)
	return present && cloud.TunneledAuthentic(r)
}

// crossTeamLimiter rate-limits per (sender board, sender, target board), so
// two Orchestrators cannot drive each other into a reply loop. It is GCRA: a
// burst of crossTeamBurst, then one message per crossTeamRefill, tracked as a
// "theoretical arrival time" per key in integer durations (no float drift).
type crossTeamLimiter struct {
	mu   sync.Mutex
	tats map[string]time.Time
	now  func() time.Time // tests inject a clock
}

// allow admits one message for key, or returns how long until one would be.
func (l *crossTeamLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.tats == nil {
		l.tats = map[string]time.Time{}
	}
	// A key whose arrival time has passed has a full burst again; drop it so
	// the map only holds senders that are currently limited.
	for k, tat := range l.tats {
		if !tat.After(now) {
			delete(l.tats, k)
		}
	}
	tat := l.tats[key]
	if tat.Before(now) {
		tat = now
	}
	if wait := tat.Sub(now) - (crossTeamBurst-1)*crossTeamRefill; wait > 0 {
		return false, wait
	}
	l.tats[key] = tat.Add(crossTeamRefill)
	return true, 0
}

// refund gives back the message allow just admitted for key, when it never went anywhere
// (a relayed send that couldn't reach Coral Hub, #3489).
func (l *crossTeamLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if tat, ok := l.tats[key]; ok {
		l.tats[key] = tat.Add(-crossTeamRefill)
	}
}

// GetBoardSettings returns a board's settings.
// GET /api/board/{project}/settings
func (h *BoardHandler) GetBoardSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.bs.GetBoardSettings(r.Context(), urlParam(r, "project"))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if cloud.Tunneled(r) {
		// The health check's configuration (its recipient, its default reviewers) is the
		// local dashboard's: Coral Hub's view of the settings leaves it out (Security's #4176
		// rule 17, on step 2).
		raw, err := json.Marshal(settings)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil {
			errInternalServer(w, "settings")
			return
		}
		delete(fields, "health")
		writeJSON(w, http.StatusOK, fields)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// UpdateBoardSettings changes a board's settings. Only the Operator (the
// dashboard's reserved identity, the same trust model as task planning) may
// change them, so an agent cannot open its own board to other teams.
// PUT /api/board/{project}/settings {"subscriber_id": "Operator", "cross_team_messaging": true}
func (h *BoardHandler) UpdateBoardSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		board.BoardSettingsUpdate
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// The Operator only, decided here and not only by the guard in front of the route: these
	// settings decide what Coral types into every agent (Security's #4176 rule 16). A claimed
	// name is not enough; an agent's token, or its command line without the Operator's
	// signature, is refused whatever subscriber_id it gives.
	// By the actor alone under enforce (#4255): the Operator the identity layer verified (a claim of
	// the Operator's name, when given, must be it); in log mode the claim-and-caller test above stands,
	// and where the two differ it is counted.
	legacy := body.SubscriberID == crossTeamOperatorID && !agentCaller(r) && !(isAgentTool(r) && !cliauth.Verified(r))
	byActor := callerActor(r).Kind == ActorOperator && (strings.TrimSpace(body.SubscriberID) == "" || board.IsOperatorName(body.SubscriberID))
	allowed := legacy
	if identityActorEnforced() {
		allowed = byActor
	} else if legacy != byActor {
		noteEnforceDiffers("board-settings", r)
	}
	if !allowed {
		errForbidden(w, "only the Operator can change board settings")
		return
	}
	if body.CrossTeamArtifacts != nil && cloud.Tunneled(r) {
		// Accepting files from other teams is decided on this computer only (#4379; coral-hub's
		// design, section 10 C3): never through Coral Hub.
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "local_only", "error": "cross_team_artifacts can be changed only on this computer, not through Coral Hub"})
		return
	}
	if body.CrossTeamServers != nil {
		var clean []string
		seen := map[string]bool{}
		for _, id := range *body.CrossTeamServers {
			id = strings.ToLower(strings.TrimSpace(id))
			if !relayIDRe.MatchString(id) {
				errBadRequest(w, fmt.Sprintf("cross_team_servers: %q is not a Coral Hub server id", id))
				return
			}
			if !seen[id] {
				seen[id] = true
				clean = append(clean, id)
			}
		}
		if len(clean) > maxCrossTeamServer {
			errBadRequest(w, fmt.Sprintf("cross_team_servers: at most %d servers", maxCrossTeamServer))
			return
		}
		body.CrossTeamServers = &clean
	}
	if body.Health != nil {
		// The check's recipient is a member of this team, never anyone else (#4176 rule 18).
		if rcpt := strings.TrimSpace(body.Health.Recipient); rcpt != "" {
			if sub, err := h.bs.GetProjectSubscription(r.Context(), urlParam(r, "project"), rcpt); err != nil || sub == nil {
				errBadRequest(w, fmt.Sprintf("health.recipient: %q is not a member of this team", board.OneLine(rcpt)))
				return
			}
		}
	}
	settings, err := h.bs.UpdateBoardSettings(r.Context(), urlParam(r, "project"), body.BoardSettingsUpdate)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// PostExternalMessage sends a cross-team message to the {project} board.
// The sender is subscriber_id on from_board; its role and display name come
// from that subscription, never from the request. It is delivered to the
// target board's Orchestrators (or the one named by "to") with an @mention,
// and mirrored on the sender's board.
// POST /api/board/{project}/external-messages
// {"from_board": "...", "subscriber_id": "...", "session_name": "...", "content": "...", "to": "..."}
func (h *BoardHandler) PostExternalMessage(w http.ResponseWriter, r *http.Request) {
	if src, present := relaySource(r); present {
		h.postRelayedExternalMessage(w, r, src)
		return
	}
	target := urlParam(r, "project")
	var body struct {
		FromBoard    string `json:"from_board"`
		SubscriberID string `json:"subscriber_id"`
		// SessionName is the caller's own session (coral-board sends
		// CORAL_SESSION_NAME). It must match the sender's subscription: a
		// speed bump against sending as another agent, not authentication;
		// local identity is trusted at the OS-user level.
		SessionName string `json:"session_name"`
		Content     string `json:"content"`
		To          string `json:"to"`
		// Attach are coral://artifacts links sent with the message (#4379): see resolveAttachments.
		Attach []string `json:"attach"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.FromBoard = strings.TrimSpace(body.FromBoard)
	switch {
	case body.Content == "":
		errBadRequest(w, "content required")
		return
	case len(body.Content) > maxCrossTeamContent:
		errBadRequest(w, fmt.Sprintf("content must be at most %d bytes", maxCrossTeamContent))
		return
	case body.FromBoard == "" || body.SubscriberID == "":
		errBadRequest(w, "from_board and subscriber_id are required")
		return
	case body.FromBoard == target:
		errBadRequest(w, "use a normal board post to message your own board")
		return
	}
	ctx := r.Context()

	// The Operator through Coral Hub (#3404): admitted by TunnelIdentityGuard with the
	// Coral Hub user, and the text says so, so the receiving Orchestrator can tell it
	// from the local Operator.
	if body.SubscriberID == crossTeamOperatorID && cloud.Tunneled(r) {
		hubUser := hubOperatorUser(r)
		if hubUser == "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required", "error": "Through Coral Hub you can't act as the Operator here"})
			return
		}
		body.Content = HubOperatorPrefix(hubUser) + body.Content
	}

	// Only Orchestrators and the Operator may send.
	role := crossTeamOperatorID
	senderSession := "" // the sending Orchestrator's agent session name
	if body.SubscriberID == crossTeamOperatorID {
		if ok, err := h.bs.BoardExists(ctx, body.FromBoard); err != nil {
			errInternalServer(w, err.Error())
			return
		} else if !ok {
			errNotFound(w, fmt.Sprintf("board %q not found", body.FromBoard))
			return
		}
	} else {
		sub, err := h.bs.GetProjectSubscription(ctx, body.FromBoard, body.SubscriberID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if sub == nil || sub.IsActive == 0 || !strings.EqualFold(strings.TrimSpace(sub.JobTitle), crossTeamSenderRole) {
			errForbidden(w, fmt.Sprintf("only an active Orchestrator of %q or the Operator can send cross-team messages", body.FromBoard))
			return
		}
		if sn := strings.TrimSpace(body.SessionName); sn == "" || sub.SessionName == "" || sn != sub.SessionName {
			errForbidden(w, fmt.Sprintf("session_name does not match %q's subscription on %q; send from that agent's own session", body.SubscriberID, body.FromBoard))
			return
		}
		role = crossTeamSenderRole
		senderSession = sub.SessionName
	}

	// An unknown target is a typo, not a disabled board.
	if ok, err := h.bs.BoardExists(ctx, target); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !ok {
		errNotFound(w, fmt.Sprintf("board %q not found", target))
		return
	}

	// Both boards must have opted in.
	var disabled []string
	for _, b := range []string{body.FromBoard, target} {
		settings, err := h.bs.GetBoardSettings(ctx, b)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if !settings.CrossTeamMessaging {
			disabled = append(disabled, strconv.Quote(b))
		}
	}
	if len(disabled) > 0 {
		errForbidden(w, fmt.Sprintf("cross-team messaging is disabled on board %s; the Operator can enable it in Team settings", strings.Join(disabled, " and ")))
		return
	}

	recipients, err := h.bs.TargetOrchestrators(ctx, target, strings.TrimSpace(body.To))
	if err != nil {
		if errors.Is(err, board.ErrNoTargetOrchestrator) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("board %q has no active Orchestrator to receive the message", target)})
			return
		}
		errNotFound(w, err.Error())
		return
	}

	if ok, wait := h.crossTeam.allow(body.FromBoard + "\x00" + body.SubscriberID + "\x00" + target); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		// A limit on this sender's posts, not a busy server (#3434).
		httputil.WriteActionLimit(w, secs, "rate_limited", fmt.Sprintf("too many messages to %q; retry in %ds", target, secs))
		return
	}

	// Attachments (#4379): both boards must accept artifacts, and every one is checked and read
	// through before anything is posted.
	var attached []attachedArtifact
	if len(body.Attach) > 0 {
		var off []string
		for _, b := range []string{body.FromBoard, target} {
			settings, err := h.bs.GetBoardSettings(ctx, b)
			if err != nil {
				errInternalServer(w, err.Error())
				return
			}
			if !settings.CrossTeamArtifacts {
				off = append(off, strconv.Quote(b))
			}
		}
		if len(off) > 0 {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "artifacts_off", "error": fmt.Sprintf("sending artifacts is off on board %s; the Operator can turn on cross_team_artifacts in Team settings on this computer", strings.Join(off, " and "))})
			return
		}
		sender := attachSender{person: body.SubscriberID == crossTeamOperatorID && callerActor(r).IsOperator(), windowKey: "operator"}
		if !sender.person {
			sender.windowKey = sharedUploadBucket
			if owner, ok := VerifiedSession(ctx); ok && owner != nil && owner.SessionID != "" && senderSession != "" && owner.SessionName == senderSession {
				sender.sessionID, sender.windowKey = owner.SessionID, "session:"+owner.SessionID
				sender.team = append([]string{owner.SessionID}, h.teamSessions(ctx, body.FromBoard)...)
			}
		}
		var refused *attachRefusal
		if attached, refused = h.resolveAttachments(body.Attach, sender); refused != nil {
			refused.write(w)
			return
		}
		body.Content += attachmentLines(attached)
	}

	inbound, outbound, err := h.bs.PostCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin: board.MessageOrigin{
			ServerID: store.LocalServerID, Board: body.FromBoard, Sender: body.SubscriberID, SenderRole: role,
			TargetServerID: store.LocalServerID, TargetBoard: target,
		},
		Mentions: recipients,
		Content:  body.Content,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.notifyFn != nil {
		// Both boards got a message: the target the inbound one, the sender's the outbound copy.
		h.notifyFn(target)
		h.notifyFn(body.FromBoard)
	}
	h.goBG(func() { h.resolveMessageWaits(target, inbound.SubscriberID, inbound.Content) })
	resp := map[string]any{"message": inbound, "mirror": outbound, "delivered_to": recipients}
	if len(attached) > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		var ids []string
		for _, a := range attached {
			base := crossTeamArtifactRecord{ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size, Server: "local", By: body.SubscriberID, At: now}
			shared, received := base, base
			shared.Board, shared.OtherBoard, shared.MessageID = body.FromBoard, target, outbound.ID
			received.Board, received.OtherBoard, received.MessageID = target, body.FromBoard, inbound.ID
			if err := h.writeCrossTeamRecord("shared", shared); err != nil {
				slog.Warn("cross-team artifact: could not record the shared row", "board", body.FromBoard, "id", a.ID, "error", err)
			}
			if err := h.writeCrossTeamRecord("received", received); err != nil {
				slog.Warn("cross-team artifact: could not record the received row", "board", target, "id", a.ID, "error", err)
			}
			ids = append(ids, "coral://artifacts/"+a.ID)
		}
		resp["attached"] = ids
	}
	writeJSON(w, http.StatusOK, resp)
}

// relayRefuse answers a relayed message with one of the fixed error codes Coral Hub maps
// (#3489): never free text from this server beyond the message.
func relayRefuse(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": code, "message": msg})
}

// postRelayedExternalMessage delivers a cross-team message that Coral Hub relayed from
// another Coral of the same account (#3489). The hub sets X-Coral-Hub-Server from the
// sending Coral's tunnel; it is trusted only on a request this process's connector
// stamped. The sender's own checks (Orchestrator or Operator, its session, its board's
// opt-in) are the sending Coral's; here the target board must have cross-team messaging
// on and list the source server in cross_team_servers (Operator-only). Only the inbound
// copy is stored (the sender keeps the mirror), de-duplicated on (source server,
// relay.message_id), and the reply names no recipients.
// POST /api/board/{project}/external-messages with X-Coral-Hub-Server
// {"from_board", "subscriber_id", "content", "to", "relay": {"message_id", "hops"}}
func (h *BoardHandler) postRelayedExternalMessage(w http.ResponseWriter, r *http.Request, src string) {
	if r.Header.Get(tunnel.HubUserHeader) != "" {
		relayRefuse(w, http.StatusBadRequest, "ambiguous_origin", "a relayed message comes from a server, never a Coral Hub user")
		return
	}
	if !cloud.TunneledAuthentic(r) {
		relayRefuse(w, http.StatusForbidden, "hub_server_untrusted", "X-Coral-Hub-Server is only accepted from Coral Hub")
		return
	}
	src = strings.ToLower(src)
	if !relayIDRe.MatchString(src) {
		relayRefuse(w, http.StatusBadRequest, "bad_request", "malformed source server")
		return
	}
	target := urlParam(r, "project")
	var body struct {
		FromBoard    string `json:"from_board"`
		SubscriberID string `json:"subscriber_id"`
		Content      string `json:"content"`
		To           string `json:"to"`
		Relay        *struct {
			MessageID string `json:"message_id"`
			Hops      int    `json:"hops"`
		} `json:"relay"`
	}
	if err := decodeJSON(r, &body); err != nil {
		relayRefuse(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.FromBoard = strings.TrimSpace(body.FromBoard)
	body.SubscriberID = strings.TrimSpace(body.SubscriberID)
	switch {
	case body.Relay == nil || !relayIDRe.MatchString(body.Relay.MessageID):
		relayRefuse(w, http.StatusBadRequest, "bad_request", "relay.message_id is required")
		return
	case body.Relay.Hops < 0 || body.Relay.Hops > maxRelayHops:
		relayRefuse(w, http.StatusBadRequest, "too_many_hops", fmt.Sprintf("a relayed reply chain is limited to %d hops", maxRelayHops))
		return
	case body.Content == "" || body.FromBoard == "" || body.SubscriberID == "":
		relayRefuse(w, http.StatusBadRequest, "bad_request", "from_board, subscriber_id and content are required")
		return
	case len(body.Content) > maxRelayedContent:
		relayRefuse(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content must be at most %d bytes", maxRelayedContent))
		return
	}
	ctx := r.Context()
	if ok, err := h.bs.BoardExists(ctx, target); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !ok {
		relayRefuse(w, http.StatusNotFound, "not_found", "no such board")
		return
	}
	settings, err := h.bs.GetBoardSettings(ctx, target)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !settings.CrossTeamMessaging {
		relayRefuse(w, http.StatusForbidden, "cross_team_disabled", "cross-team messaging is off on this board")
		return
	}
	if !settings.AllowsServer(src) {
		relayRefuse(w, http.StatusForbidden, "source_not_allowed", "this board doesn't accept messages from that server")
		return
	}
	recipients, err := h.bs.TargetOrchestrators(ctx, target, strings.TrimSpace(body.To))
	if err != nil {
		relayRefuse(w, http.StatusConflict, "no_orchestrator", "no active Orchestrator to receive the message")
		return
	}
	if ok, wait := h.crossTeam.allow("relay\x00" + src + "\x00" + body.FromBoard + "\x00" + body.SubscriberID + "\x00" + target); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		// A limit on this sender's messages, not a busy server (#3434).
		httputil.WriteActionLimitBody(w, secs, map[string]any{"ok": false, "error": "rate_limited", "message": fmt.Sprintf("retry in %ds", secs)})
		return
	}
	role := crossTeamSenderRole
	if body.SubscriberID == crossTeamOperatorID {
		role = crossTeamOperatorID
	}
	inbound, duplicate, err := h.bs.PostRelayedCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin: board.MessageOrigin{
			ServerID: src, Board: body.FromBoard, Sender: body.SubscriberID, SenderRole: role,
			TargetServerID: store.LocalServerID, TargetBoard: target,
			RelayMessageID: body.Relay.MessageID, Hops: body.Relay.Hops,
		},
		Mentions: recipients,
		Content:  body.Content,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Ids, sizes and the outcome only: never the content (as Coral Hub's audit).
	slog.Info("[cloud] relayed cross-team message", "source_server", src, "board", target, "bytes", len(body.Content), "duplicate", duplicate)
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true, "target_message_id": inbound.ID})
		return
	}
	if h.notifyFn != nil {
		h.notifyFn(target)
	}
	h.goBG(func() { h.resolveMessageWaits(target, inbound.SubscriberID, inbound.Content) })
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target_message_id": inbound.ID})
}
