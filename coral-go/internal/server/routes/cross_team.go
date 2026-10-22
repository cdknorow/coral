package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
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
	if r.Method != http.MethodPost || !(strings.HasSuffix(r.URL.Path, "/external-messages") || strings.HasSuffix(r.URL.Path, "/external-artifacts")) {
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
	// burst and refill override crossTeamBurst and crossTeamRefill when set (the coarse relay limiters, #5514).
	burst  int
	refill time.Duration
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
	burst, refill := int64(crossTeamBurst), crossTeamRefill
	if l.burst > 0 && l.refill > 0 {
		burst, refill = int64(l.burst), l.refill
	}
	if wait := tat.Sub(now) - time.Duration(burst-1)*refill; wait > 0 {
		return false, wait
	}
	l.tats[key] = tat.Add(refill)
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
		w.Header().Set("Cache-Control", "no-store") // the owner's reply holds the health setup (#5307)
	}
	if cloud.Tunneled(r) && !hubOwnerRequest(r).owner {
		// The health check's configuration (its recipient, its default reviewers) is the
		// local dashboard's: Coral Hub's view of the settings leaves it out (Security's #4176
		// rule 17, on step 2), except for this computer's owner (#5307), who sets it through the hub.
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

// boardSettingsHubMax is the largest body a board settings save accepts through Coral Hub (a health setup is well under 2 KB).
const boardSettingsHubMax = 16 << 10

// UpdateBoardSettings changes a board's settings. Only the Operator (the
// dashboard's reserved identity, the same trust model as task planning) may
// change them, so an agent cannot open its own board to other teams.
// PUT /api/board/{project}/settings {"subscriber_id": "Operator", "cross_team_messaging": true}
func (h *BoardHandler) UpdateBoardSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		board.BoardSettingsUpdate
	}
	if cloud.Tunneled(r) {
		// Through Coral Hub this is the owner's alone, decided before the body is read (#5307), and the body is strict: one JSON object of at
		// most boardSettingsHubMax bytes whose every key (the health setup's included) is known.
		if refuseNonOwner(w, r) { // the table's owner route (PUT /api/board/{project}/settings)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, boardSettingsHubMax))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "too_large", "error": "the body is larger than the 16 KB a board settings save accepts through Coral Hub"})
				return
			}
			errBadRequest(w, "the body is unreadable")
			return
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			errBadRequest(w, "invalid JSON: only subscriber_id and the known settings are accepted")
			return
		}
		if _, err := dec.Token(); err != io.EOF {
			errBadRequest(w, "invalid JSON: one object only")
			return
		}
	} else if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// An edit of who may message this board (the list, or the old switch that is now a view of it) that is refused for ANY reason is counted: one audit row an hour per board, ids and a
	// count, never text (#5508, coral-hub's SPEC-2). A refusal by the Operator gate, by the hub, by validation or by the edit limit all count.
	touchesList := body.CrossTeamAllowed != nil || body.CrossTeamMessaging != nil
	listEditRefused := touchesList
	defer func() {
		if listEditRefused {
			h.noteEditRefused(r.Context(), urlParam(r, "project"))
		}
	}()
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
	if cloud.Tunneled(r) {
		// Only the health check's setup (#5307): the cross-team fields (who may send to this board, which servers, whether files are
		// accepted) stay on this computer (#3377, #4379).
		if body.CrossTeamMessaging != nil || body.CrossTeamServers != nil || body.CrossTeamArtifacts != nil || body.CrossTeamAllowed != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "local_only", "error": "cross-team settings can be changed only on this computer, not through Coral Hub"})
			return
		}
	}
	if body.CrossTeamArtifacts != nil && cloud.Tunneled(r) {
		writeError( // Accepting files from other teams is decided on this computer only (#4379; coral-hub's
			// design, section 10 C3): never through Coral Hub.
			w, http.StatusForbidden, "local_only", "cross_team_artifacts can be changed only on this computer, not through Coral Hub")
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
	project := urlParam(r, "project")
	var before board.BoardSettings
	if body.CrossTeamAllowed != nil {
		// The WHOLE list, checked as a whole (#5508): the owner's edit limit first, then each entry's shape, the cap, duplicates and the board's own id; one transaction applies it.
		if !h.allowEditSlot(callerActor(r)) {
			httputil.WriteActionLimit(w, 0, "edit_rate_limited", "too many edits of who may message this board in the last hour; try again later") // marked X-Coral-Limit: action like every per-action limit (limit census)
			return
		}
		self, _ := h.senderIDOf(r.Context(), project)
		clean, row, field, msg := board.AllowedProblem(*body.CrossTeamAllowed, self)
		if field != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("cross_team_allowed entry %d, %s: %s", row+1, field, msg), "code": "bad_allowlist", "row": row, "field": field, "message": msg})
			return
		}
		body.CrossTeamAllowed = &clean
		before, _ = h.bs.GetBoardSettings(r.Context(), project)
	}
	var beforeSwitches board.BoardSettings
	touchesSwitch := body.CrossTeamMessaging != nil || body.CrossTeamArtifacts != nil
	if touchesSwitch {
		beforeSwitches, _ = h.bs.GetBoardSettings(r.Context(), project)
	}
	settings, err := h.bs.UpdateBoardSettings(r.Context(), project, body.BoardSettingsUpdate)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	listEditRefused = false
	if touchesSwitch {
		// So "last changed by" on the Messaging tab never names a hub user after the owner here changed the switch again (#5572).
		ctx := context.WithoutCancel(r.Context())
		if body.CrossTeamMessaging != nil && *body.CrossTeamMessaging != beforeSwitches.CrossTeamMessaging {
			_ = h.bs.NoteSwitchChangeLocal(ctx, project, board.SwitchMessaging, beforeSwitches.CrossTeamMessaging, *body.CrossTeamMessaging)
		}
		if body.CrossTeamArtifacts != nil && *body.CrossTeamArtifacts != beforeSwitches.CrossTeamArtifacts {
			_ = h.bs.NoteSwitchChangeLocal(ctx, project, board.SwitchFiles, beforeSwitches.CrossTeamArtifacts, *body.CrossTeamArtifacts)
		}
	}
	if body.CrossTeamAllowed != nil {
		// Every successful edit is audited by id: who (the verified Operator identity), when, and the list before and after (ids only).
		actor := callerActor(r).Name
		if actor == "" {
			actor = crossTeamOperatorID
		}
		if err := h.bs.AppendEditLog(context.WithoutCancel(r.Context()), project, actor, "cross_team_allowed", allowedForLog(before.CrossTeamAllowed), allowedForLog(settings.CrossTeamAllowed)); err != nil {
			slog.Warn("cross-team: could not log a list edit", "board", project, "error", err)
		}
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
	var body externalMessage
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.FromBoard = strings.TrimSpace(body.FromBoard)
	if refused := body.refusal(target); refused != nil {
		refused(w)
		return
	}
	ctx := r.Context()
	if refused := body.hubOperatorPrefix(r); refused != nil {
		refused(w)
		return
	}
	role, senderSession, refused := h.crossTeamSenderRole(ctx, &body)
	if refused != nil {
		refused(w)
		return
	}
	recipients, rateTaken, refused := h.crossTeamAdmit(ctx, &body, role, target)
	if refused != nil {
		refused(w)
		return
	}
	if !rateTaken { // the allowlist's door takes the token itself, before it decides (#5508, so a refused send costs what an accepted one does)
		if secs, limited := h.crossTeamWait(body.FromBoard + "\x00" + body.SubscriberID + "\x00" + target); limited {
			// A limit on this sender's posts, not a busy server (#3434).
			httputil.WriteActionLimit(w, secs, "rate_limited", fmt.Sprintf("too many messages to %q; retry in %ds", target, secs))
			return
		}
	}
	attached, refused := h.crossTeamAttachments(ctx, r, &body, target, senderSession)
	if refused != nil {
		refused(w)
		return
	}
	inbound, outbound, err := h.bs.PostCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin: board.MessageOrigin{
			ServerID: store.LocalServerID, Board: body.FromBoard, Sender: body.SubscriberID, SenderRole: role,
			TargetServerID: store.LocalServerID, TargetBoard: target,
			FromCrossTeamID: func() string { id, _ := h.senderIDOf(ctx, body.FromBoard); return id }(),
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
		resp["attached"] = h.recordCrossTeamArtifacts(attached, body.FromBoard, target, body.SubscriberID, outbound.ID, inbound.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// externalMessage is a PostExternalMessage request.
type externalMessage struct {
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

// refusal refuses a message with no text, text over the limit, no sender, or sent to its own board.
func (m *externalMessage) refusal(target string) answer {
	switch {
	case m.Content == "":
		return func(w http.ResponseWriter) { errBadRequest(w, "content required") }
	case len(m.Content) > maxCrossTeamContent:
		return func(w http.ResponseWriter) {
			errBadRequest(w, fmt.Sprintf("content must be at most %d bytes", maxCrossTeamContent))
		}
	case m.FromBoard == "" || m.SubscriberID == "":
		return func(w http.ResponseWriter) { errBadRequest(w, "from_board and subscriber_id are required") }
	case m.FromBoard == target:
		return func(w http.ResponseWriter) { errBadRequest(w, "use a normal board post to message your own board") }
	}
	return nil
}

// hubOperatorPrefix: the Operator through Coral Hub (#3404) is admitted by TunnelIdentityGuard with the Coral Hub
// user, and the text says so, so the receiving Orchestrator can tell it from the local Operator. Without that user
// it is refused.
func (m *externalMessage) hubOperatorPrefix(r *http.Request) answer {
	if m.SubscriberID != crossTeamOperatorID || !cloud.Tunneled(r) {
		return nil
	}
	hubUser := hubOperatorUser(r)
	if hubUser == "" {
		return func(w http.ResponseWriter) {
			writeError(w, http.StatusForbidden, "operator_identity_required", "Through Coral Hub you can't act as the Operator here")
		}
	}
	m.Content = HubOperatorPrefix(hubUser) + m.Content
	return nil
}

// crossTeamSenderRole is the sender's role and session on its board: only its active Orchestrators and the Operator
// may send, from the sender's own session.
func (h *BoardHandler) crossTeamSenderRole(ctx context.Context, m *externalMessage) (string, string, answer) {
	role, senderSession, refusal, err := h.checkCrossTeamSender(ctx, m.FromBoard, m.SubscriberID, m.SessionName)
	switch {
	case err != nil:
		return "", "", func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	case refusal == senderNoBoard:
		return "", "", func(w http.ResponseWriter) { errNotFound(w, fmt.Sprintf("board %q not found", m.FromBoard)) }
	case refusal == senderNotOrchestrator:
		return "", "", func(w http.ResponseWriter) {
			errForbidden(w, fmt.Sprintf("only an active Orchestrator of %q or the Operator can send cross-team messages", m.FromBoard))
		}
	case refusal == senderSessionMismatch:
		return "", "", func(w http.ResponseWriter) {
			errForbidden(w, fmt.Sprintf("session_name does not match %q's subscription on %q; send from that agent's own session", m.SubscriberID, m.FromBoard))
		}
	}
	return role, senderSession, nil
}

// crossTeamRecipients is who receives the message on the target board: its active Orchestrators, or the one named
// by to. The target must exist (an unknown one is a typo, not a disabled board), and both boards must have opted in.
func (h *BoardHandler) crossTeamRecipients(ctx context.Context, from, target, to string) ([]string, answer) {
	if ok, err := h.bs.BoardExists(ctx, target); err != nil {
		return nil, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	} else if !ok {
		return nil, func(w http.ResponseWriter) { errNotFound(w, fmt.Sprintf("board %q not found", target)) }
	}
	var disabled []string
	for _, b := range []string{from, target} {
		settings, err := h.bs.GetBoardSettings(ctx, b)
		if err != nil {
			return nil, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
		}
		if !settings.ExplicitMessagingOn() { // an explicit opt-in: no default opens two teams of one computer to each other (#5916)
			disabled = append(disabled, strconv.Quote(b))
		}
	}
	if len(disabled) > 0 {
		return nil, func(w http.ResponseWriter) {
			errForbidden(w, textCrossTeamOff(disabled))
		}
	}
	recipients, err := h.bs.TargetOrchestrators(ctx, target, strings.TrimSpace(to))
	if err != nil {
		if errors.Is(err, board.ErrNoTargetOrchestrator) {
			return nil, func(w http.ResponseWriter) {
				errConflict(w, fmt.Sprintf("board %q has no active Orchestrator to receive the message", target))
			}
		}
		return nil, func(w http.ResponseWriter) { errNotFound(w, err.Error()) }
	}
	return recipients, nil
}

// crossTeamAttachments (#4379): both boards must accept artifacts, and every one is checked and read through before
// anything is posted; their links are appended to the message.
func (h *BoardHandler) crossTeamAttachments(ctx context.Context, r *http.Request, m *externalMessage, target, senderSession string) ([]attachedArtifact, answer) {
	if len(m.Attach) == 0 {
		return nil, nil
	}
	var off []string
	for _, b := range []string{m.FromBoard, target} {
		settings, err := h.bs.GetBoardSettings(ctx, b)
		if err != nil {
			return nil, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
		}
		if !settings.ExplicitFilesOn() {
			off = append(off, strconv.Quote(b))
		}
	}
	if len(off) > 0 {
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusForbidden, "artifacts_off", textFilesOff(off))
		}
	}
	sender := h.attachSenderFor(ctx, r, m.FromBoard, m.SubscriberID, senderSession)
	attached, refused := h.resolveAttachments(m.Attach, sender)
	if refused != nil {
		return nil, refused.write
	}
	m.Content += attachmentLines(attached)
	return attached, nil
}

// recordCrossTeamArtifacts writes each attached artifact's shared row (on the sender's board, with the mirror) and
// received row (on the target board, with the message), and returns their links.
func (h *BoardHandler) recordCrossTeamArtifacts(attached []attachedArtifact, from, target, by string, outboundID, inboundID int64) []string {
	now := time.Now().UTC().Format(time.RFC3339)
	var ids []string
	var listed []board.MessageAttachment
	for _, a := range attached {
		base := crossTeamArtifactRecord{ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size, Server: "local", By: by, At: now}
		shared, received := base, base
		shared.Board, shared.OtherBoard, shared.MessageID = from, target, outboundID
		received.Board, received.OtherBoard, received.MessageID = target, from, inboundID
		if err := h.writeCrossTeamRecord("shared", shared); err != nil {
			slog.Warn("cross-team artifact: could not record the shared row", "board", from, "id", a.ID, "error", err)
		}
		if err := h.writeCrossTeamRecord("received", received); err != nil {
			slog.Warn("cross-team artifact: could not record the received row", "board", target, "id", a.ID, "error", err)
		}
		ids = append(ids, "coral://artifacts/"+a.ID)
		listed = append(listed, board.MessageAttachment{ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size})
	}
	h.listAttachmentsOnMessage(target, inboundID, listed) // the message's own envelope lists them (#5506)
	return ids
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
		// FromCrossTeamID is the sending Coral's claim of its board's cross-team id (#5508): checked for its shape before anything compares it, never echoed.
		FromCrossTeamID string `json:"from_cross_team_id"`
		Relay           *struct {
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
	case !plainLabel(body.FromBoard): // shown as the message's origin: bounded plain text, as a received file's from_board already is (#4901)
		relayRefuse(w, http.StatusBadRequest, "bad_request", "from_board must be plain text of at most 200 bytes")
		return
	case len(body.Content) > maxRelayedContent:
		relayRefuse(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content must be at most %d bytes", maxRelayedContent))
		return
	}
	ctx := r.Context()
	// The sender's rate token FIRST and for every path, keyed on the source and what it says it is, before anything about the target is looked up: a 429 that only an existing team could
	// earn would be the probe the uniform refusal removes (Security's condition 9, #5572). A refused send costs a token as an accepted one does.
	if ok, wait := h.relayRateAllow(src, body.FromBoard, body.SubscriberID, target); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		httputil.WriteActionLimitBody(w, secs, map[string]any{"ok": false, "error": "rate_limited", "message": fmt.Sprintf("retry in %ds", secs)})
		return
	}
	// Then ONE door, the same reads in the same order whatever the answer. A relayed sender is never the local Operator, whatever it says its role is, and the sending Coral's board id is a
	// claim checked for its shape before it is compared (#5508).
	sender := doorSender{server: src, crossTeamID: validIDOrEmpty(body.FromCrossTeamID), relayed: true}
	var settings board.BoardSettings
	var recipients []string
	var admitted bool
	var err error
	mode := doorOld // which door took it, for the shadow count below (the board's server list is read only by the old one)
	if h.allowlistOn(ctx) {
		recipients, settings, admitted, err = h.crossTeamDoor(ctx, target, sender, strings.TrimSpace(body.To))
	} else {
		// The door follows what the connection's hub advertised (#5862): cross_team_links/1 admits by the hub's link snapshot; a hub that advertised it before and no longer does is refused
		// (never sent back to the old door); a hub that never did keeps the board's server list as it always was.
		mode = h.DoorMode(ctx, r)
		switch mode {
		case doorLinks:
			recipients, settings, admitted, err = h.linksDoor(ctx, target, src, hubSourceTeam(r), strings.TrimSpace(body.To), false)
		case doorRefuse:
			recipients, settings, admitted, err = h.linksDoor(ctx, target, src, hubSourceTeam(r), strings.TrimSpace(body.To), true)
		default:
			recipients, settings, admitted, err = h.relayedDoor(ctx, target, src, strings.TrimSpace(body.To))
		}
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !admitted {
		if h.allowlistOn(ctx) {
			h.noteDoor(ctx, target, board.LogRefused, sender)
		}
		// One answer for every cause (no such team, not linked, switched off, nobody to tell): the same status, code, text and size (coral-hub's requirement 5).
		writeRelayedRefused(w)
		return
	}
	if !h.allowlistOn(ctx) && mode == doorOld && !sender.listed(settings) {
		// Shadow (#5508): what the list would have refused, counted, not acted on.
		h.noteDoor(ctx, target, board.LogWouldRefuse, sender)
	}
	role := crossTeamSenderRole
	if body.SubscriberID == crossTeamOperatorID {
		role = crossTeamOperatorID
	}
	inbound, duplicate, err := h.bs.PostRelayedCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin: board.MessageOrigin{
			ServerID: src, Board: body.FromBoard, Sender: body.SubscriberID, SenderRole: role,
			TargetServerID: store.LocalServerID, TargetBoard: target,
			RelayMessageID: body.Relay.MessageID, Hops: body.Relay.Hops, FromCrossTeamID: validIDOrEmpty(body.FromCrossTeamID),
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
	// The files this message names (pushed before it, with its message id) are listed now, if the board
	// still takes files (#4440).
	if settings.CrossTeamArtifacts {
		if on, err := h.receivingOn(ctx, r, target, src, validIDOrEmpty(body.FromCrossTeamID), true); err == nil && on {
			h.promotePending(target, src, body.Relay.MessageID, inbound.ID)
		}
	}
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
