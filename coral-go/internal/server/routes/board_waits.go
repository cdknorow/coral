// Waits: registering, polling and resolving them (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/board"
)

// RegisterWait parks an agent by registering an active wait condition.
// POST /api/board/{project}/waits
func (h *BoardHandler) RegisterWait(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionName  string `json:"session_name,omitempty"`
		WaitType     string `json:"wait_type"`
		TargetID     string `json:"target_id"`
		Reason       string `json:"reason"`
		Timeout      string `json:"timeout,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if body.SubscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if refuseTextTooLarge(w, "reason_too_large", "a wait's reason", len(body.Reason), maxWaitReasonBytes) {
		return
	}
	if body.WaitType == board.WaitOperator {
		// A question for the Operator: the reason is the question. It is shown to the
		// orchestrator only through teamhealth.ShownText, and is kept short here.
		body.TargetID = "Operator"
		// Only a current member asks, so a made-up name can't add lines to the check.
		if sub, err := h.bs.GetProjectSubscription(r.Context(), project, body.SubscriberID); err != nil || sub == nil {
			errBadRequest(w, "only a member of this board can ask the Operator")
			return
		}
		body.Reason = strings.TrimSpace(body.Reason)
		if body.Reason == "" {
			errBadRequest(w, "a question for the Operator needs its text")
			return
		}
		if r := []rune(body.Reason); len(r) > 500 {
			body.Reason = string(r[:500])
		}
	}
	if body.SessionName == "" {
		if sub, err := h.bs.GetProjectSubscription(r.Context(), project, body.SubscriberID); err == nil && sub != nil {
			body.SessionName = sub.SessionName
		}
	}
	timeout := board.DefaultWaitTimeout
	if body.Timeout != "" {
		d, err := time.ParseDuration(body.Timeout)
		if err != nil || d <= 0 {
			msg := "timeout must be a duration such as 30m or 2h"
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg, "message": msg, "code": "invalid_timeout", "not_saved": true})
			return
		}
		timeout = d
	}
	wait, err := h.bs.RegisterWait(r.Context(), project, body.SubscriberID, body.SessionName, body.WaitType, body.TargetID, body.Reason, timeout)
	if errors.Is(err, board.ErrTooManyWaits) {
		msg := err.Error()
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "message": msg, "code": "too_many_waits", "max": board.MaxActiveWaitsPerSubscriber, "not_saved": true})
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, wait)
}

// GetActiveWait returns active registered wait(s) for a subscriber or board.
// GET /api/board/{project}/waits
func (h *BoardHandler) GetActiveWait(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID != "" {
		attrWho, attrOK := attributedTo(w, r, subscriberID)
		if !attrOK {
			return
		}
		subscriberID = attrWho
		wait, err := h.bs.GetActiveWait(r.Context(), project, subscriberID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"wait": wait})
		return
	}
	waits, err := h.bs.ListActiveWaits(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"waits": waits})
}

// CancelWait cancels an active wait for a subscriber.
// DELETE /api/board/{project}/waits
func (h *BoardHandler) CancelWait(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	subscriberID := r.URL.Query().Get("subscriber_id")
	if subscriberID == "" {
		var body struct {
			SubscriberID string `json:"subscriber_id"`
		}
		_ = decodeJSON(r, &body)
		subscriberID = body.SubscriberID
	}
	attrWho, attrOK := attributedTo(w, r, subscriberID)
	if !attrOK {
		return
	}
	subscriberID = attrWho
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if err := h.bs.CancelActiveWait(r.Context(), project, subscriberID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// PollWait long-polls until an active wait is resolved, expired, or cancelled.
// GET /api/board/{project}/waits/poll
func (h *BoardHandler) PollWait(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	subscriberID, attrOK := attributedTo(w, r, r.URL.Query().Get("subscriber_id"))
	if !attrOK {
		return
	}
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	timeoutSec := 30
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
			if sec > 60 {
				sec = 60
			}
			timeoutSec = sec
		}
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	// Sleep until a wait changes in the store (resolve, cancel, expiry), the
	// wait's own expiry, or the deadline, instead of querying every 500ms per
	// waiter. waitPollRecheck is a safety net for a change the signal missed.
	for {
		changed := h.bs.WaitsChanged() // before the read, so no change slips between
		wait, err := h.bs.GetActiveWait(r.Context(), project, subscriberID)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if wait == nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "resolved"})
			return
		}
		now := time.Now()
		if now.After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"status": "timeout", "wait": wait})
			return
		}
		next := min(deadline.Sub(now), waitPollRecheck)
		if expires, err := time.Parse(time.RFC3339, wait.ExpiresAt); err == nil {
			// GetActiveWait expires it once the second has passed.
			next = min(next, expires.Add(time.Second).Sub(now))
		}
		timer := time.NewTimer(max(next, 10*time.Millisecond))
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// waitPollRecheck bounds how long a long-poll sleeps without re-reading its
// wait, in case a change did not signal (for example, an older writer).
var waitPollRecheck = 5 * time.Second

func (h *BoardHandler) resolveMessageWaits(project, senderID, content string) {
	ctx := context.Background()
	resolved, err := h.bs.ResolveMatchingMessageWaits(ctx, project, senderID, content)
	if err != nil || len(resolved) == 0 {
		return
	}
	preview := content
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			// The sender is quoted like the preview (#3376); sendAutomated keeps it one line.
			nudge := fmt.Sprintf("[Wait resolved] %q posted on '%s': %q. Run 'coral-board read' to see the message.", senderID, project, preview)
			if err := h.sendAutomated(ctx, project, sessionName, nudge); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}

func (h *BoardHandler) resolveTaskWaits(project string, taskID int64, taskTitle, event string) {
	ctx := context.Background()
	resolved, err := h.bs.ResolveMatchingTaskWaits(ctx, project, taskID)
	if err != nil || len(resolved) == 0 {
		return
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			nudge := fmt.Sprintf("[Wait resolved] Task #%d (%s) is now %s. Run 'coral-board task detail %d' to review.", taskID, taskTitle, event, taskID)
			if err := h.sendAutomated(ctx, project, sessionName, nudge); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}

func (h *BoardHandler) ResolveCommitWaits(ctx context.Context, project, commitHash string) {
	resolved, err := h.bs.ResolveMatchingCommitWaits(ctx, project, commitHash)
	if err != nil || len(resolved) == 0 {
		return
	}
	for _, w := range resolved {
		sessionName := w.SessionName
		if sub, err := h.bs.GetProjectSubscription(ctx, project, w.SubscriberID); err == nil && sub != nil && sub.SessionName != "" {
			sessionName = sub.SessionName
		}
		if h.terminal != nil && sessionName != "" {
			nudge := fmt.Sprintf("[Wait resolved] Commit %s landed on '%s'.", commitHash, project)
			if err := h.sendAutomated(ctx, project, sessionName, nudge); err != nil {
				slog.Warn("failed to nudge waiting agent", "subscriber", w.SubscriberID, "session", sessionName, "error", err)
			}
		}
	}
}
