package routes

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/ptymanager"
	setreg "github.com/subgentic/coral/internal/settings"
)

// TurnEndHandoffSetting turns the turn-end hand-off off when "off" (#3481, #3475 3A).
// It is on by default, for Claude agents on a board (team agents Coral launches);
// an agent with no board, which a person drives, never gets it.
const TurnEndHandoffSetting = "turn_end_handoff"

// turnEndHandoffWait bounds how long the hand-off waits for a send in progress to the
// agent before it gives up (and hands nothing off): the agent's Stop hook is waiting.
const turnEndHandoffWait = 1500 * time.Millisecond

// lastDelivered is when a send with a key was last typed into a session, zero if never
// (ptymanager); a variable so tests can set it.
var lastDelivered = ptymanager.LastDelivered

// TurnEndHandoff is called by a Claude agent's Stop hook when its turn ends (#3481,
// #3475 3A). If the agent has unread board messages, the answer tells the hook to keep
// the agent going with them: Claude Code continues with the hook's reason and context,
// so nothing is typed into its terminal and no prompt can be answered by mistake (a turn
// ends with no request open). The nudges Coral holds for the same messages are retired
// at the same time (ptymanager.TakeHeld), so they are never typed afterwards.
//
// Only the agent's own hook may call it (its session token, in every mode), once per
// stop chain (stop_hook_active: Claude's own guard against a hook that always blocks).
// POST /api/sessions/live/{name}/turn-end
func (h *SessionsHandler) TurnEndHandoff(w http.ResponseWriter, r *http.Request) {
	owner, hasToken := VerifiedSession(r.Context())
	if !hasToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_token_required",
			"message": "The turn-end hand-off is for the agent's own Stop hook, with its session token"})
		return
	}
	var body struct {
		StopHookActive bool `json:"stop_hook_active"`
	}
	_ = decodeJSON(r, &body)
	none := map[string]any{"handoff": false}
	if body.StopHookActive {
		writeJSON(w, http.StatusOK, none)
		return
	}
	ctx := r.Context()
	ls, err := h.ss.GetLiveSession(ctx, owner.SessionID)
	if err != nil || ls == nil || ls.AgentType != at.Claude || ls.BoardName == nil || *ls.BoardName == "" || h.bs == nil {
		writeJSON(w, http.StatusOK, none)
		return
	}
	if settings, _ := h.ss.GetSettings(ctx); setreg.Enum(settings, TurnEndHandoffSetting) == "off" {
		writeJSON(w, http.StatusOK, none)
		return
	}
	board := *ls.BoardName
	counts, err := h.bs.GetUnreadCountsForSessions(ctx, []string{owner.SessionName})
	unread := counts[owner.SessionName]
	if err != nil || unread <= 0 {
		writeJSON(w, http.StatusOK, none)
		return
	}
	nudgeKey := "board-unread:" + board
	// A nudge for these messages typed DURING this turn is queued in Claude's input and
	// arrives when the turn ends, so a hand-off would deliver them twice (#3510: with the
	// screen rule, the default, a nudge isn't held during a turn). The nudge that started
	// the turn was typed before its prompt_submit, so it doesn't count. The turn starts at its
	// FIRST prompt_submit: Claude records another one for the queued nudge itself.
	if typed := lastDelivered(owner.SessionName, nudgeKey); !typed.IsZero() {
		if start, ok, err := h.ts.TurnStartTime(ctx, owner.SessionID); err == nil && ok && typed.After(start) {
			slog.Info("turn-end hand-off: skipped, a nudge was typed during the turn", "session", owner.SessionName, "board", board, "unread", unread)
			writeJSON(w, http.StatusOK, none)
			return
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, turnEndHandoffWait)
	defer cancel()
	taken, ok := ptymanager.TakeHeld(waitCtx, owner.SessionName, "handed off", func(key string) bool { return key == nudgeKey })
	if !ok {
		// A send to the agent was in progress and didn't finish in time: the nudge is
		// about to be typed anyway, so no hand-off (it would be a second copy).
		writeJSON(w, http.StatusOK, none)
		return
	}
	plural := "s"
	if unread == 1 {
		plural = ""
	}
	slog.Info("turn-end hand-off: the agent goes on with its board messages", "session", owner.SessionName, "board", board, "unread", unread, "retired_nudges", len(taken))
	writeJSON(w, http.StatusOK, map[string]any{
		"handoff": true,
		// Claude shows the reason as "Stop hook error: <reason>" whatever it says
		// (verified on 2.1.300), so it is kept short; the instruction is the context,
		// shown as "Stop hook feedback".
		"reason":  "New board messages",
		"context": fmt.Sprintf("Coral board: %d new message%s on %s. Run coral-board read to read them, then carry on.", unread, plural, board),
	})
}
