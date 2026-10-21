// Projects, subscribing and unsubscribing, listing subscribers and peeking at an agent (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
)

// ListProjects returns all boards with subscriber and message counts.
// GET /api/board/projects
func (h *BoardHandler) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.bs.ListProjects(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(projects))
}

// Subscribe subscribes to a board.
// POST /api/board/{project}/subscribe
// Accepts subscriber_id (stable identity) with optional session_name.
func (h *BoardHandler) Subscribe(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string  `json:"subscriber_id"`
		SessionName  string  `json:"session_name"`
		JobTitle     string  `json:"job_title"`
		WebhookURL   *string `json:"webhook_url"`
		ReceiveMode  string  `json:"receive_mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// Webhooks were removed (#3646): a subscription naming one is refused, so nobody thinks it
	// applies.
	if body.WebhookURL != nil && *body.WebhookURL != "" {
		errBadRequest(w, errWebhooksNotSupported)
		return
	}
	if refuseNewBoardName(w, r, h.bs, project) {
		return
	}
	// Through Coral Hub the subscriber is the Coral Hub user the connector names, never a name
	// from the body: the dashboard asks for "Operator", which made the Hub's viewer the board's
	// Operator subscription (#3942). Like a chat post (#3377): no header is 400, a name already
	// on the board 409; no local session, the default mode (mentions), and recorded.
	if cloud.Tunneled(r) {
		user, _, status, msg := h.tunnelChatSender(r, project)
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": msg, "code": "hub_user_required"})
			return
		}
		if strings.EqualFold(strings.TrimSpace(body.ReceiveMode), "all") {
			writeError(w, http.StatusForbidden, "orchestrator_identity",
				"Through Coral Hub a subscriber can't receive every message (receive_mode all is the orchestrator's)")
			return
		}
		sub, err := h.bs.Subscribe(r.Context(), project, user, hubSubscriberTitle, "", nil, body.ReceiveMode)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		recordHubOperatorAction(r.Context(), h.bs, user, r.Method, r.URL.Path, project, http.StatusOK)
		writeJSON(w, http.StatusOK, sub)
		return
	}
	joinName, joinSession, joinOK := attributedJoin(w, r, body.SubscriberID, body.SessionName)
	if !joinOK {
		return
	}
	body.SubscriberID, body.SessionName = joinName, joinSession
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if body.JobTitle == "" {
		body.JobTitle = "Agent"
	}
	sub, err := h.bs.Subscribe(r.Context(), project, subscriberID, body.JobTitle, body.SessionName, nil, body.ReceiveMode)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// hubSubscriberTitle is the role a Coral Hub user's board subscription shows (#3942).
const hubSubscriberTitle = "Coral Hub user"

// Unsubscribe removes a subscriber from a board.
// DELETE /api/board/{project}/subscribe
func (h *BoardHandler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedSubject(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	found, err := h.bs.Unsubscribe(r.Context(), project, subscriberID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !found {
		errNotFound(w, "subscriber not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListSubscribers returns subscribers for a board.
// GET /api/board/{project}/subscribers
func (h *BoardHandler) ListSubscribers(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	subs, err := h.bs.ListSubscribers(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(subs))
}

// PeekAgent captures terminal output of another agent on the same board.
// GET /api/board/{project}/peek?target=<name>&subscriber_id=<caller>&lines=30
func (h *BoardHandler) PeekAgent(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	callerID := r.URL.Query().Get("subscriber_id")
	if !cloud.Tunneled(r) { // through Coral Hub the caller is never read from the query
		attrWho, attrOK := attributedTo(w, r, callerID)
		if !attrOK {
			return
		}
		callerID = attrWho
	}
	target := r.URL.Query().Get("target")
	lines := queryInt(r, "lines", 30)

	if lines > 500 {
		lines = 500
	}

	// Through Coral Hub (SEC-TUN-02, #3410) the caller's identity is never taken from the
	// query: a Hub user at View may read every agent's output (operator decision), but not
	// by claiming to be an orchestrator, so subscriber_id is ignored, and peeking never
	// touches task state.
	tunneled := cloud.Tunneled(r)
	if target == "" || (callerID == "" && !tunneled) {
		errBadRequest(w, "subscriber_id and target are required")
		return
	}

	// Check caller has can_peek permission — use project-scoped lookup
	// to prevent cross-board authorization bypass.
	allSubs, err := h.bs.ListSubscribers(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	if !tunneled {
		// Find caller in this board's subscribers
		var caller *board.Subscriber
		for i, s := range allSubs {
			if s.SubscriberID == callerID {
				caller = &allSubs[i]
				break
			}
		}
		if caller == nil {
			errForbidden(w, "not subscribed to this board")
			return
		}
		if caller.CanPeek == 0 {
			errForbidden(w, "peek permission not granted for this subscriber")
			return
		}
	}

	// Find target in same board's subscribers
	var targetSub *board.Subscriber
	for i, s := range allSubs {
		if s.SubscriberID == target || s.JobTitle == target {
			targetSub = &allSubs[i]
			break
		}
	}
	if targetSub == nil {
		errNotFound(w, "target subscriber not found on this board")
		return
	}
	// A peek counts as activity on the target's task, but not one a browser sends from
	// another page (an image tag elsewhere could keep a stalled task looking active; #3511
	// follow-up). Agents' coral-board peek sends no fetch metadata.
	if site := r.Header.Get("Sec-Fetch-Site"); !tunneled && (site == "" || site == "same-origin" || site == "none") {
		_ = h.bs.TouchActiveTask(r.Context(), project, targetSub.SubscriberID)
	}

	if h.terminal == nil {
		errInternalServer(w, "terminal backend not available")
		return
	}

	// Capture terminal output using the target's session name
	output, err := h.terminal.CaptureOutput(r.Context(), targetSub.SessionName, lines, "", "")
	if err != nil {
		slog.Warn("peek capture failed", "target", target, "error", err)
		errInternalServer(w, "failed to capture terminal output")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"target":       target,
		"session_name": targetSub.SessionName,
		"lines":        lines,
		"output":       lastLines(output, lines),
	})
}

// lastLines is peek's "the last N lines of their terminal" (#27): a capture is N rows of scrollback
// above the whole screen on both backends (tmux's capture-pane -S -N, the PTY's rendered ring), so a
// peek of 30 lines on a 50-row screen gave 80. A capture within n lines is returned exactly as it
// was (what a peek of a small tmux pane always gave); a longer one has the blank rows under its last
// output dropped first, so the lines are the agent's latest, not the empty bottom of its screen.
func lastLines(text string, n int) string {
	if n <= 0 || strings.Count(text, "\n") < n {
		return text // no count asked for (lines=0), or already within it: unchanged
	}
	rows := strings.Split(strings.TrimRight(text, " \t\r\n"), "\n")
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}
