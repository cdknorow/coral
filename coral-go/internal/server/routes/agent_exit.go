package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safefs"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// Agents that died on their own (#3613 A, GitHub issue #25). When a team hits its account's
// rate limit, its CLIs stop one after another and used to just lie asleep. Now:
//   - The exit notice: once per team per burst (the deaths within exitBurstWindow of the first),
//     one dashboard notification and one board post from Coral's Health Monitor, with no
//     @-mention (an Orchestrator reading everything sees it; workers aren't nudged). It names
//     the agents (cleaned, capped, no "@") and why, as a fixed label from the agent's last turn
//     end: "rate limited", "overloaded", "API error" or "exited". The CLI's own error text never
//     leaves the server (Security).
//   - Auto-resume (Settings, off by default): an agent stopped by an API error is woken through
//     the same wake as the dashboard's, after about 2, 5 then 15 minutes (±20%). The attempts are
//     counted in a file in the data folder; after the third, Coral stops trying for good, until
//     a person wakes the agent (only that resets the count: never an agent's wake).
// The reconciler finds the deaths (background.SessionReconciler.SetOnSlept); one notice goes out
// per death, since an agent asleep is never found dead again until it is woken.

const (
	// AutoResumeSetting is Settings > "Resume agents stopped by an API error": "true", or off.
	AutoResumeSetting   = "auto_resume_after_api_error"
	exitBurstWindow     = time.Minute
	autoResumeFile      = "auto_resume_attempts.json"
	exitNoticeSender    = "Coral Health Monitor"
	exitNoticeMaxAgents = 10
)

// autoResumeDelays: before the first, second and third attempt.
var autoResumeDelays = []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

type exitWatch struct {
	mu      sync.Mutex
	pending map[string][]string // team ("" = no board) -> the burst's session IDs
	// after runs f once d has passed (tests replace it).
	after func(d time.Duration, f func())
}

func defaultAfter(d time.Duration, f func()) { time.AfterFunc(d, f) }

// exitLabel is the fixed label for how an agent's last turn ended (failed: by StopFailure,
// with the CLI's text).
func exitLabel(text string, failed bool) string {
	if !failed {
		return "exited"
	}
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "429") || strings.Contains(t, "rate limit") || strings.Contains(t, "rate_limit"):
		return "rate limited"
	case strings.Contains(t, "529") || strings.Contains(t, "overloaded"):
		return "overloaded"
	}
	return "API error"
}

// noticeName: the agent's name for a notice (its board title, else its display name), cleaned,
// capped and with no "@", so a notice never mentions anyone.
func (h *SessionsHandler) noticeName(ctx context.Context, ls store.LiveSession) string {
	name := ""
	if h.bs != nil && ptr.Value(ls.BoardName) != "" {
		if sub, err := h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(ls.AgentType, ls.SessionID)); err == nil && sub != nil {
			name = cleanAgentName(sub.JobTitle)
		}
	}
	if name == "" {
		name = agentLabel(ls)
	}
	return strings.TrimSpace(strings.ReplaceAll(name, "@", ""))
}

// AgentsSlept hears the agents the reconciler just found dead. The team's notice goes out
// exitBurstWindow after the first of a burst.
func (h *SessionsHandler) AgentsSlept(ctx context.Context, sessionIDs []string) {
	for _, sid := range sessionIDs {
		ls, err := h.ss.GetLiveSession(ctx, sid)
		if err != nil || ls == nil {
			continue
		}
		team := ptr.Value(ls.BoardName)
		h.exits.mu.Lock()
		if h.exits.pending == nil {
			h.exits.pending = map[string][]string{}
		}
		first := len(h.exits.pending[team]) == 0
		h.exits.pending[team] = append(h.exits.pending[team], sid)
		h.exits.mu.Unlock()
		if first {
			h.exits.after(exitBurstWindow, func() { h.flushExits(context.Background(), team) })
		}
	}
}

type exitedAgent struct {
	ls    store.LiveSession
	name  string
	label string
	plan  string // what happens next
}

// flushExits sends a team's burst notice and schedules its auto-resumes.
func (h *SessionsHandler) flushExits(ctx context.Context, team string) {
	h.exits.mu.Lock()
	ids := h.exits.pending[team]
	delete(h.exits.pending, team)
	h.exits.mu.Unlock()

	settings, _ := h.ss.GetSettings(ctx)
	resumeOn := setreg.Bool(settings, AutoResumeSetting)
	attempts := h.autoResumeAttempts()

	var agents []exitedAgent
	seen := map[string]bool{}
	for _, sid := range ids {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		ls, err := h.ss.GetLiveSession(ctx, sid)
		if err != nil || ls == nil || ls.IsSleeping != 1 || ls.IsArchived == 1 {
			continue // woken, archived or ended within the window
		}
		text, failed := "", false
		if h.ts != nil {
			text, failed, _ = h.ts.LatestStopFailure(ctx, sid)
		}
		a := exitedAgent{ls: *ls, name: h.noticeName(ctx, *ls), label: exitLabel(text, failed), plan: "Wake resumes it"}
		if failed && resumeOn {
			if n := attempts[sid]; n < len(autoResumeDelays) {
				d := ptymanager.JitterAround(autoResumeDelays[n], fmt.Sprintf("%s/%d", sid, n))
				a.plan = fmt.Sprintf("Coral resumes it in about %d min (attempt %d of %d)", int(d.Round(time.Minute)/time.Minute), n+1, len(autoResumeDelays))
				h.exits.after(d, func() { h.autoResume(context.Background(), sid, n+1) })
			} else {
				a.plan = fmt.Sprintf("Coral tried %d times and has stopped; Wake resumes it", n)
			}
		}
		agents = append(agents, a)
	}
	if len(agents) == 0 {
		return
	}
	h.postExitNotice(ctx, team, agents)
}

func exitNoticeLines(agents []exitedAgent) []string {
	var lines []string
	for i, a := range agents {
		if i == exitNoticeMaxAgents {
			lines = append(lines, fmt.Sprintf("and %d more", len(agents)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("%s: %s. %s.", a.name, a.label, a.plan))
	}
	return lines
}

func (h *SessionsHandler) postExitNotice(ctx context.Context, team string, agents []exitedAgent) {
	head := "1 agent stopped"
	if len(agents) > 1 {
		head = fmt.Sprintf("%d agents stopped", len(agents))
	}
	lines := exitNoticeLines(agents)
	if h.notifications != nil {
		msg := strings.Join(lines, " ")
		if t := cleanAgentName(team); t != "" {
			msg = "On " + t + ": " + msg
		}
		h.notifications.Push(Notification{Title: head, Message: msg, Level: "warning"})
	}
	if team != "" && h.bs != nil {
		content := head + ":\n- " + strings.Join(lines, "\n- ")
		if _, err := h.bs.PostSystemMessage(ctx, team, exitNoticeSender, content, nil); err != nil {
			slog.Warn("[agent-exit] notice not posted", "board", team, "err", err)
		}
	}
}

// AutoResumeNotice is the fixed text of Coral's board notice to an agent it resumed.
const AutoResumeNotice = "Coral resumed you after your CLI stopped on an API error. Check your last step before you continue."

// autoResume is attempt n (1-based) for an agent. It does nothing when the agent was woken,
// archived or ended meanwhile, the setting was turned off, or a person's wake reset the
// count (a stale timer).
func (h *SessionsHandler) autoResume(ctx context.Context, sid string, n int) {
	settings, _ := h.ss.GetSettings(ctx)
	if !setreg.Bool(settings, AutoResumeSetting) {
		return
	}
	ls, err := h.ss.GetLiveSession(ctx, sid)
	if err != nil || ls == nil || ls.IsSleeping != 1 || ls.IsArchived == 1 {
		return
	}
	if h.autoResumeAttempts()[sid] != n-1 {
		return
	}
	// Counted before the wake: a wake that takes Coral down still counts.
	h.setAutoResumeAttempt(sid, n)
	if err := h.wakeExistingSession(ctx, ls); err != nil {
		slog.Warn("[agent-exit] auto-resume failed", "session_id", sid, "attempt", n, "err", err)
		return
	}
	slog.Info("[agent-exit] auto-resumed", "session_id", sid, "attempt", n)
	if title := h.noticeName(ctx, *ls); title != "" && h.bs != nil && ptr.Value(ls.BoardName) != "" {
		if _, err := h.bs.PostSystemMessage(ctx, ptr.Value(ls.BoardName), board.CoralSender, "@"+title+" "+AutoResumeNotice, nil); err != nil {
			slog.Warn("[agent-exit] resume notice not posted", "err", err)
		}
	}
}

// The attempt counts, in Coral's own file in the data folder (through a root: never a link).
var autoResumeMu sync.Mutex

func (h *SessionsHandler) autoResumeAttempts() map[string]int {
	out := map[string]int{}
	root, err := safefs.OpenRoot(h.cfg.CoralDir())
	if err != nil {
		return out
	}
	defer root.Close()
	if b, err := root.ReadFile(autoResumeFile, 256<<10); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// setAutoResumeAttempt stores n for sid; 0 forgets it.
func (h *SessionsHandler) setAutoResumeAttempt(sid string, n int) {
	autoResumeMu.Lock()
	defer autoResumeMu.Unlock()
	m := h.autoResumeAttempts()
	if n == 0 {
		if _, ok := m[sid]; !ok {
			return
		}
		delete(m, sid)
	} else {
		m[sid] = n
	}
	b, _ := json.Marshal(m)
	if root, err := safefs.OpenRoot(h.cfg.CoralDir()); err == nil {
		_ = root.WriteFile(autoResumeFile, b, 0o600)
		root.Close()
	}
}

// resetAutoResume: the Operator woke the agent, so Coral may try again the next time.
func (h *SessionsHandler) resetAutoResume(sid string) { h.setAutoResumeAttempt(sid, 0) }

// validateAutoResumeSetting checks the setting in a settings PUT: a boolean.
func validateAutoResumeSetting(body map[string]any) error {
	v, ok := body[AutoResumeSetting]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case bool:
		body[AutoResumeSetting] = fmt.Sprint(x)
		return nil
	case string:
		if x == "true" || x == "false" {
			return nil
		}
	}
	return fmt.Errorf("%s must be true or false", AutoResumeSetting)
}
