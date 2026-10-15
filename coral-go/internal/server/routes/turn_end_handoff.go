package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// TurnEndHandoffSetting turns the turn-end hand-off off when "off" (#3481, #3475 3A).
// It is on by default, for Claude agents on a board (team agents Coral launches);
// an agent with no board, which a person drives, never gets it.
const TurnEndHandoffSetting = "turn_end_handoff"

// turnEndHandoffWait bounds how long the hand-off waits for a send in progress to the
// agent before it gives up (and hands nothing off): the agent's Stop hook is waiting.
const turnEndHandoffWait = 1500 * time.Millisecond

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
	_ = json.NewDecoder(r.Body).Decode(&body)
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
	if settings, _ := h.ss.GetSettings(ctx); strings.EqualFold(strings.TrimSpace(settings[TurnEndHandoffSetting]), "off") {
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
	waitCtx, cancel := context.WithTimeout(ctx, turnEndHandoffWait)
	defer cancel()
	nudgeKey := "board-unread:" + board
	taken := ptymanager.TakeHeld(waitCtx, owner.SessionName, func(key string) bool { return key == nudgeKey })
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
