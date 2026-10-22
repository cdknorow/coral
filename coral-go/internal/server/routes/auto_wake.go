package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/clitext"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/safefs"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// Waking the agents that were running when a pty Coral stopped (#3636). Coral's own shutdown
// and startup code marks them asleep with the reason coral_stopped (never a request: an
// Operator's Sleep is reason ""). At the next start this pass wakes them one at a time through
// the same wake as the dashboard's Wake (each CLI's resume, its stored flags and permission
// mode, the worktree, the board, the paste gate), unless:
//   - the setting is off;
//   - more than the cap (default 8) were running: the dashboard asks first, and its Wake them
//     calls the per-agent wake route, staggered;
//   - the passes look like a crash loop: one under 10 minutes ago, or 3 in the last hour.
// A board agent gets one notice from Coral, addressed to it alone.

const (
	// AutoWakeSetting is Settings > "Wake agents that were running when Coral stopped": "true"
	// (the default) or "false".
	AutoWakeSetting = setreg.AutoWakeAfterRestart
	// AutoWakeCapSetting is how many agents Coral wakes without asking, 1 to 50 (default 8).
	AutoWakeCapSetting = setreg.AutoWakeCap
	maxAutoWakeCap     = 50

	autoWakeStartDelay = 5 * time.Second // the server listens before an agent resumes
	autoWakeGap        = 2 * time.Second // between two wakes, plus up to autoWakeJitter
	autoWakeJitter     = time.Second
	autoWakeRecent     = 10 * time.Minute // crash-loop guard: a pass this recent skips the next
	autoWakeHourMax    = 3                // or this many passes in the last hour
	autoWakeRunsFile   = "auto_wake_runs.json"
)

// AutoWakeNotice is the fixed text of Coral's board notice to a woken agent.
const AutoWakeNotice = "Coral restarted; your last turn may not have finished. Check your last step before you continue."

type autoWakeAgent struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

type autoWakeFailure struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// autoWakeStatus is GET /api/system/auto-wake. State: "" (nothing to do), "waking", "done",
// "pending" (over the cap: the Operator decides), "off" (the setting) or "guarded" (crash loop).
type autoWakeStatus struct {
	State   string            `json:"state"`
	Pending []autoWakeAgent   `json:"pending"`
	Woken   int               `json:"woken"`
	Failed  []autoWakeFailure `json:"failed"`
	Waiting int               `json:"waiting"` // agents asleep because Coral stopped (off or guarded)
}

type autoWaker struct {
	mu     sync.Mutex
	status autoWakeStatus
	// sleep waits d, or reports false when ctx ends first; now is the clock (tests replace both).
	sleep func(ctx context.Context, d time.Duration) bool
	now   func() time.Time
}

func (a *autoWaker) snapshot() autoWakeStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.status
	st.Pending = append([]autoWakeAgent{}, st.Pending...)
	st.Failed = append([]autoWakeFailure{}, st.Failed...)
	return st
}

func (a *autoWaker) set(f func(*autoWakeStatus)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(&a.status)
}

// autoWakeOn: the setting, on unless it says "false".
func autoWakeOn(settings map[string]string) bool {
	return setreg.Bool(settings, AutoWakeSetting)
}

func autoWakeCap(settings map[string]string) int {
	return int(setreg.Int(settings, AutoWakeCapSetting))
}

// validateAutoWakeSettings checks the two settings in a settings PUT (Security's point d):
// a boolean, and a whole number from 1 to 50. Values are normalised to strings.
func validateAutoWakeSettings(body map[string]any) error {
	if v, ok := body[AutoWakeSetting]; ok {
		switch x := v.(type) {
		case bool:
			body[AutoWakeSetting] = strconv.FormatBool(x)
		case string:
			if x != "true" && x != "false" {
				return errors.New(AutoWakeSetting + " must be true or false")
			}
		default:
			return errors.New(AutoWakeSetting + " must be true or false")
		}
	}
	if v, ok := body[AutoWakeCapSetting]; ok {
		var n int
		switch x := v.(type) {
		case float64:
			if x != float64(int(x)) {
				return errors.New(AutoWakeCapSetting + " must be a whole number from 1 to 50")
			}
			n = int(x)
		case string:
			var err error
			if n, err = strconv.Atoi(strings.TrimSpace(x)); err != nil {
				return errors.New(AutoWakeCapSetting + " must be a whole number from 1 to 50")
			}
		default:
			return errors.New(AutoWakeCapSetting + " must be a whole number from 1 to 50")
		}
		if n < 1 || n > maxAutoWakeCap {
			return errors.New(AutoWakeCapSetting + " must be a whole number from 1 to 50")
		}
		body[AutoWakeCapSetting] = strconv.Itoa(n)
	}
	return nil
}

// cleanAgentName makes a name safe to show in a notice (Security's point e and NIT): the shared
// name cleaner (clitext.CleanName: controls, bidi and every format character, one line, #3648),
// capped at 80.
func cleanAgentName(s string) string {
	out := clitext.CleanName(s)
	if r := []rune(out); len(r) > 80 {
		out = string(r[:80])
	}
	return out
}

// coralStoppedSessions: asleep because Coral stopped, not archived, newest first.
func (h *SessionsHandler) coralStoppedSessions(ctx context.Context) []store.LiveSession {
	all, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		return nil
	}
	var out []store.LiveSession
	for _, ls := range all {
		// hooks_update: Coral stopped it to resume it on the current hooks (#3665) and didn't get to.
		if ls.IsSleeping == 1 && ls.IsArchived == 0 && ls.SleepReason != nil && (*ls.SleepReason == store.SleepReasonCoralStopped || *ls.SleepReason == store.SleepReasonHooksUpdate) {
			out = append(out, ls)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func agentLabel(ls store.LiveSession) string {
	if n := cleanAgentName(ptr.Value(ls.DisplayName)); n != "" {
		return n
	}
	if n := cleanAgentName(ls.AgentName); n != "" {
		return n
	}
	return ls.AgentType
}

// autoWakeRuns reads the recent passes (Coral's own file in the data folder, through a root:
// never a link).
func (h *SessionsHandler) autoWakeRuns() []time.Time {
	root, err := safefs.OpenRoot(h.cfg.CoralDir())
	if err != nil {
		return nil
	}
	defer root.Close()
	b, err := root.ReadFile(autoWakeRunsFile, 64<<10)
	if err != nil {
		return nil
	}
	var runs []time.Time
	_ = json.Unmarshal(b, &runs)
	return runs
}

// autoWakeGuarded: the crash-loop guard (Security's point b).
func (h *SessionsHandler) autoWakeGuarded() (bool, string) {
	now := h.autoWake.now()
	recent := 0
	var last time.Time
	for _, t := range h.autoWakeRuns() {
		if now.Sub(t) < time.Hour {
			recent++
		}
		if t.After(last) {
			last = t
		}
	}
	if !last.IsZero() && now.Sub(last) < autoWakeRecent {
		return true, "Coral woke its agents less than 10 minutes ago"
	}
	if recent >= autoWakeHourMax {
		return true, fmt.Sprintf("Coral already woke its agents %d times in the last hour", recent)
	}
	return false, ""
}

func (h *SessionsHandler) recordAutoWakeRun() {
	now := h.autoWake.now()
	runs := []time.Time{now}
	for _, t := range h.autoWakeRuns() {
		if now.Sub(t) < time.Hour {
			runs = append(runs, t)
		}
	}
	b, _ := json.Marshal(runs)
	if root, err := safefs.OpenRoot(h.cfg.CoralDir()); err == nil {
		_ = root.WriteFile(autoWakeRunsFile, b, 0o600)
		root.Close()
	}
}

func (h *SessionsHandler) notifyAutoWake(msg string) {
	if h.notifications != nil {
		h.notifications.Push(Notification{Title: "Coral restarted", Message: msg, Level: "info"})
	}
}

// AutoWakeAfterRestart is the pass, once per start, on pty only (startup calls it).
func (h *SessionsHandler) AutoWakeAfterRestart(ctx context.Context) {
	if !h.autoWake.sleep(ctx, autoWakeStartDelay) {
		return
	}
	cands := h.coralStoppedSessions(ctx)
	if len(cands) == 0 {
		return
	}
	settings, _ := h.ss.GetSettings(ctx)
	n := len(cands)
	plural := map[bool]string{true: "s were", false: " was"}[n != 1]
	if !autoWakeOn(settings) {
		h.autoWake.set(func(s *autoWakeStatus) { s.State, s.Waiting = "off", n })
		h.notifyAutoWake(fmt.Sprintf("%d agent%s running when Coral stopped. Waking them is off in Settings: wake them from the list.", n, plural))
		return
	}
	if guarded, why := h.autoWakeGuarded(); guarded {
		h.autoWake.set(func(s *autoWakeStatus) { s.State, s.Waiting = "guarded", n })
		slog.Warn("[auto-wake] skipped: possible crash loop", "why", why, "agents", n)
		h.notifyAutoWake(fmt.Sprintf("%d agent%s running when Coral stopped. Not woken: %s. Wake them from the list.", n, plural, why))
		return
	}
	if limit := autoWakeCap(settings); n > limit {
		pending := make([]autoWakeAgent, 0, n)
		for _, ls := range cands {
			pending = append(pending, autoWakeAgent{SessionID: ls.SessionID, Name: agentLabel(ls)})
		}
		h.autoWake.set(func(s *autoWakeStatus) { s.State, s.Pending = "pending", pending })
		h.notifyAutoWake(fmt.Sprintf("%d agents were running when Coral stopped. Wake them? (Over the %d Coral wakes by itself.)", n, limit))
		return
	}
	h.recordAutoWakeRun()
	h.autoWake.set(func(s *autoWakeStatus) { s.State = "waking" })
	woken := 0
	var failed []autoWakeFailure
	for i, ls := range cands {
		if i > 0 && !h.autoWake.sleep(ctx, autoWakeGap+time.Duration(rand.Int64N(int64(autoWakeJitter)))) {
			return // Coral is stopping
		}
		if reason := h.autoWakeOne(ctx, ls); reason != "" {
			failed = append(failed, autoWakeFailure{Name: agentLabel(ls), Reason: reason})
			continue
		}
		woken++
		h.autoWake.set(func(s *autoWakeStatus) { s.Woken = woken })
	}
	h.autoWake.set(func(s *autoWakeStatus) { s.State, s.Woken, s.Failed = "done", woken, failed })
	msg := fmt.Sprintf("Woke %d agent%s that %s running when Coral stopped.", woken, map[bool]string{true: "s", false: ""}[woken != 1], map[bool]string{true: "were", false: "was"}[woken != 1])
	if len(failed) > 0 {
		msg += fmt.Sprintf(" %d couldn't be woken: %s.", len(failed), failed[0].Name+": "+failed[0].Reason)
	}
	h.notifyAutoWake(msg)
}

// autoWakeOne wakes one agent the way the dashboard's Wake does, then sends its board notice.
// It returns "" or why it couldn't.
func (h *SessionsHandler) autoWakeOne(ctx context.Context, ls store.LiveSession) string {
	if !isDir(ls.WorkingDir) {
		return "its folder is gone"
	}
	if err := h.wakeExistingSession(ctx, &ls); err != nil {
		if errors.Is(err, ErrStillInOldTmux) {
			return "it is still running in Coral's old tmux server"
		}
		slog.Warn("[auto-wake] wake failed", "session_id", ls.SessionID, "err", err)
		return "the wake failed"
	}
	// Resume the board as the dashboard's Wake does, or the woken agent reads nothing: Coral paused it at startup for its
	// sleeping agents (RestoreSleepingBoards). Only that pause: one a person set with Pause stays (#286).
	if bn := ptr.Value(ls.BoardName); bn != "" && h.boardHandler != nil {
		if h.boardHandler.LiftSleepPause(bn) {
			slog.Info("[auto-wake] resumed the board", "board", bn)
		}
	}
	h.sendAutoWakeNotice(ctx, ls)
	return ""
}

// sendAutoWakeNotice posts Coral's fixed notice to a woken board agent, addressed to it alone
// (CoralSender: a subscriber reading everything counts it only when it is named).
func (h *SessionsHandler) sendAutoWakeNotice(ctx context.Context, ls store.LiveSession) {
	boardName := ptr.Value(ls.BoardName)
	if boardName == "" || h.bs == nil {
		return
	}
	sub, err := h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(ls.AgentType, ls.SessionID))
	if err != nil || sub == nil {
		return
	}
	title := cleanAgentName(sub.JobTitle)
	if title == "" {
		return
	}
	if _, err := h.bs.PostSystemMessage(ctx, boardName, board.CoralSender, "@"+title+" "+AutoWakeNotice, nil); err != nil {
		slog.Warn("[auto-wake] notice not posted", "board", boardName, "err", err)
	}
}

// takePendingAutoWake removes a session from the over-the-cap list when the Operator wakes it,
// reporting whether it was there (its wake then gets the notice).
func (h *SessionsHandler) takePendingAutoWake(sessionID string) bool {
	found := false
	h.autoWake.set(func(s *autoWakeStatus) {
		for i, a := range s.Pending {
			if a.SessionID == sessionID {
				s.Pending = append(s.Pending[:i], s.Pending[i+1:]...)
				found = true
				break
			}
		}
		if found && len(s.Pending) == 0 {
			s.State = "done"
		}
	})
	return found
}

// AutoWakeStatus: GET /api/system/auto-wake (this computer's dashboard; never through Coral Hub).
func (h *SessionsHandler) AutoWakeStatus(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		errForbidden(w, "not through Coral Hub")
		return
	}
	writeJSON(w, http.StatusOK, h.autoWake.snapshot())
}

// DismissAutoWake: POST /api/system/auto-wake/dismiss, "Not now": the agents stay asleep.
func (h *SessionsHandler) DismissAutoWake(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) || !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can answer this")
		return
	}
	h.autoWake.set(func(s *autoWakeStatus) { s.Pending, s.State = nil, "done" })
	writeJSON(w, http.StatusOK, h.autoWake.snapshot())
}
