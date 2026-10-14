package routes

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
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
)

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

// GetBoardSettings returns a board's settings.
// GET /api/board/{project}/settings
func (h *BoardHandler) GetBoardSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.bs.GetBoardSettings(r.Context(), urlParam(r, "project"))
	if err != nil {
		errInternalServer(w, err.Error())
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
	if body.SubscriberID != crossTeamOperatorID {
		errForbidden(w, "only the Operator can change board settings")
		return
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
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("too many messages to %q; retry in %ds", target, secs)})
		return
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
	go h.dispatchWebhooks(target, inbound.SubscriberID, inbound)
	if h.notifyFn != nil {
		h.notifyFn()
	}
	go h.resolveMessageWaits(target, inbound.SubscriberID, inbound.Content)
	writeJSON(w, http.StatusOK, map[string]any{"message": inbound, "mirror": outbound, "delivered_to": recipients})
}
