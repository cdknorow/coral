package routes

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/sessionstate"
	"github.com/subgentic/coral/internal/store"
)

// An agent that needs the user to sign in (#3830). The CLI's own record says so first: Claude
// Code writes an API error into its transcript when a turn fails (authentication_failed,
// "Invalid API key · Please run /login", an expired or revoked OAuth token). Before the
// transcript exists (a first run, waiting at the login screen) only the screen shows it, so the
// screen is read then and only then (#3475: CLI signals over screen parsing).

// loginScreenTTL: how long a screen read stays good. The list, the WebSocket feed, the gate and
// the chat poll all ask; the screen is read at most once per agent per TTL.
const loginScreenTTL = 3 * time.Second

// loginScreenStampMax: how long a screen read stays good while the agent's terminal has written
// nothing since (its output stamp is unchanged, #4028): an agent that waits on a screen costs no
// capture-pane at all, not one every loginScreenTTL. Still bounded, in case a stamp misses a change.
const loginScreenStampMax = time.Minute

// loginScreenTimeout bounds one screen read.
const loginScreenTimeout = 2 * time.Second

// loginScreens is the last screen reading per agent, for agents with no transcript yet.
type loginScreens struct {
	mu sync.Mutex
	m  map[string]loginScreenRead
	// stampDecides: a reading taken with another output stamp is never reused, however young: the
	// terminal has written since (startup prompts, #4543, whose state must follow the screen).
	stampDecides bool
}

type loginScreenRead struct {
	at    time.Time
	stamp string // the output stamp read before the screen was ("" without one)
	on    bool
	why   string
	// confirmed: the reading before this one had the same stamp too, so the stamp has held across a whole loginScreenTTL. Only then is the reading reused past the TTL for as long
	// as the stamp holds (#5838): the stamp is as coarse as a second, and the first read of a new agent can fall in the same second as the login menu is drawn, with the stamp
	// unchanged; reusing that reading for a minute is how the sign-in card came a minute late.
	confirmed bool
}

// read returns the reading for key: the last one while it is younger than loginScreenTTL, or
// while stamp (the agent's output stamp, read before calling) is the one it was taken with and
// it is younger than loginScreenStampMax; else fn's, kept with stamp. An empty stamp is never
// matched.
func (l *loginScreens) read(key, stamp string, now time.Time, fn func() (bool, string)) (bool, string) {
	l.mu.Lock()
	if r, ok := l.m[key]; ok {
		age := now.Sub(r.at)
		changed := l.stampDecides && stamp != "" && r.stamp != stamp
		if !changed && (age < loginScreenTTL || (stamp != "" && r.confirmed && r.stamp == stamp && age < loginScreenStampMax)) {
			l.mu.Unlock()
			return r.on, r.why
		}
	}
	prev, hadPrev := l.m[key]
	l.mu.Unlock()
	on, why := fn()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]loginScreenRead{}
	}
	if len(l.m) >= 256 {
		for k, r := range l.m {
			if now.Sub(r.at) >= loginScreenStampMax {
				delete(l.m, k)
			}
		}
	}
	l.m[key] = loginScreenRead{at: now, stamp: stamp, on: on, why: why, confirmed: hadPrev && stamp != "" && prev.stamp == stamp && prev.on == on}
	return on, why
}

// forget drops key's reading: the next read takes the screen again.
func (l *loginScreens) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

// claudeLoginState reports whether a Claude agent needs the user to sign in, and the CLI's
// message when it gave one. events returns the session's state events, oldest first; it is
// called only when the transcript holds an authentication error.
func (h *SessionsHandler) claudeLoginState(name, sessionID, workingDir string, events func() []store.AgentEvent) (bool, string) {
	if sessionID == "" {
		return false, ""
	}
	apiErr, hasTranscript := h.jsonl.ReadClaudeAPIError(sessionID, workingDir)
	if hasTranscript {
		if !apiErr.NeedsLogin() || signedInSince(events(), apiErr.At) {
			return false, ""
		}
		return true, capCLIText(apiErr.Text)
	}
	reader, ok := h.terminal.(ptymanager.ScreenReader)
	if !ok || name == "" {
		return false, ""
	}
	// The output stamp, read before the screen: a read is reused while nothing was written since.
	stamp := ""
	if stamper, _ := h.backend.(ptymanager.OutputStamper); stamper != nil {
		if st, ok := stamper.OutputStamp(at.Claude, sessionID); ok {
			stamp = st
		}
	}
	on, _ := h.loginScreens.read(promptReadKey(sessionID, name, at.Claude), stamp, time.Now(), func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), loginScreenTimeout)
		defer cancel()
		var screen string
		var err error
		if stamp != "" {
			// A stamped (tmux) screen is the pane's last 40 lines, as AgentScreen reads it: through
			// the capture the poll and the team status share (#4077).
			screen, err = h.sharedCapture(ctx, name, 40, at.Claude, sessionID)
		} else {
			screen, err = reader.AgentScreen(ctx, name, at.Claude, sessionID)
		}
		if err != nil {
			return false, ""
		}
		return ptymanager.ClaudeLoginOnScreen(screen)
	})
	return on, ""
}

// usageLimitText is the notice shown when the agent hit its usage limit, or "". It comes from
// the CLI's own error, never from any text that mentions a limit (#3830: the old check matched
// "usage limit" anywhere in the terminal's last lines, and showed it on a login screen):
// Claude's transcript rate_limit error, with the CLI's message; Codex's exact limit message
// as the last thing in its terminal. A login that is pending wins: the caller asks only when
// none is.
func (h *SessionsHandler) usageLimitText(agentType, sessionID, workingDir string, logInfo map[string]any) string {
	switch agentType {
	case at.Claude, "":
		if sessionID == "" {
			return ""
		}
		if apiErr, ok := h.jsonl.ReadClaudeAPIError(sessionID, workingDir); ok && apiErr.UsageLimit() {
			return capCLIText(claudeLimitText(apiErr.Text))
		}
	case at.Codex:
		return codexLimitText(logInfo)
	case at.Agy, at.Antigravity, at.Gemini:
		return h.agyLimitText(sessionID, workingDir, logInfo)
	}
	return ""
}

// agyLimitRe: Antigravity's rate-limit, resource-exhausted and quota messages, from real 1.4.2 CLI output.
var agyLimitRe = regexp.MustCompile(`(?i)^[\s\x{25A0}\x{2022}\x{258C}\x{2502}>\x{25CB}\x{25CF}\x{2503}\x{26A0}\x{FE0F}!\-–—*]*(resource has been exhausted\b.*|you have exhausted your quota\b.*|you've exceeded your current quota\b.*|quota exceeded\b.*|allocation quota reached\b.*|individual quota reached\b.*|rate limit (?:reached|exceeded)\b.*|rate limit\b.*|too many requests\b.*)`)

var agyEventLimitRe = regexp.MustCompile(`(?i)\b(quota[ _]?exceeded|resource[ _]?exhausted|rate[ _]?limit[ _]?exceeded|stop_reason_quota_exhausted)\b`)

const agyLimitLines = 10

func (h *SessionsHandler) agyLimitText(sessionID, workingDir string, logInfo map[string]any) string {
	// CLI signals over screen parsing rule: check hooks/events and transcript first
	if notice := h.agySignalLimitText(sessionID, workingDir); notice != "" {
		return capCLIText(notice)
	}
	// Fall back to terminal screen text
	return agyScreenLimitText(logInfo)
}

func (h *SessionsHandler) agySignalLimitText(sessionID, workingDir string) string {
	if sessionID == "" {
		return ""
	}
	// Check newest state event (e.g. Stop hook with termination reason)
	if h != nil && h.ts != nil {
		if events, err := h.ts.GetSessionStateEvents(context.Background(), []string{sessionID}); err == nil {
			if evs := events[sessionID]; len(evs) > 0 {
				for i := len(evs) - 1; i >= 0; i-- {
					e := evs[i]
					if e.EventType == "stop" {
						detail := ""
						if e.DetailJSON != nil {
							detail = *e.DetailJSON
						}
						if notice := agyReasonToNotice(e.Summary, detail); notice != "" {
							return notice
						}
						break
					}
				}
			}
		}
	}
	// Check transcript error
	if h != nil && h.jsonl != nil {
		if errText, ok := h.jsonl.ReadAgyAPIError(sessionID, workingDir); ok && errText != "" {
			if agyLimitRe.MatchString(errText) || agyEventLimitRe.MatchString(errText) {
				return errText
			}
		}
	}
	return ""
}

func agyReasonToNotice(summary, detailJSON string) string {
	if detailJSON != "" {
		var d struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(detailJSON), &d) == nil {
			if d.Error != "" && agyLimitRe.MatchString(d.Error) {
				return d.Error
			}
			if d.Reason != "" && agyEventLimitRe.MatchString(d.Reason) {
				return agyReasonNoticeText(d.Reason)
			}
		}
	}
	if m := agyEventLimitRe.FindString(summary); m != "" {
		return agyReasonNoticeText(m)
	}
	return ""
}

func agyReasonNoticeText(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "quota"):
		return "You have exhausted your quota on this model."
	case strings.Contains(r, "resource"):
		return "Resource has been exhausted (e.g. check quota)."
	case strings.Contains(r, "rate"):
		return "Rate limit reached. Please try again later."
	default:
		return "This agent hit its usage limit and cannot continue until the limit resets."
	}
}

func agyScreenLimitText(logInfo map[string]any) string {
	lines, _ := logInfo["recent_lines"].([]string)
	if len(lines) > agyLimitLines {
		lines = lines[len(lines)-agyLimitLines:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if m := agyLimitRe.FindStringSubmatch(lines[i]); m != nil {
			text := strings.TrimSpace(m[1])
			// At a narrow width the CLI wraps the reset time ("... Resets in" / "103h13m54s."): it belongs to the notice.
			if i+1 < len(lines) && agyResetTailRe.MatchString(lines[i+1]) {
				text += " " + strings.TrimSpace(lines[i+1])
			}
			return capCLIText(text)
		}
	}
	return ""
}

// agyResetTailRe is the wrapped end of agy's quota notice: only the reset time, "103h13m54s.".
var agyResetTailRe = regexp.MustCompile(`^\s*(?:\d+[hms])+\.?\s*$`)

// claudeLimitText is a rate-limit error's text for the notice. Older CLIs wrote "Claude AI usage
// limit reached|<unix time>".
func claudeLimitText(text string) string {
	if before, _, ok := strings.Cut(text, "|"); ok && strings.HasPrefix(text, "Claude AI usage limit reached") {
		text = before
	}
	if text == "" {
		return "This agent hit its usage limit and cannot continue until the limit resets."
	}
	return text
}

// codexLimitRe: Codex's limit message, "■ You've hit your usage limit. Upgrade to ..." (the
// texts in codex-cli 0.155.1's binary all start so). Its idle screen's "You have 1 usage limit
// reset available" is not one.
var codexLimitRe = regexp.MustCompile(`^[\s\x{25A0}\x{2022}\x{258C}\x{2502}>]*(You've hit your usage limit\b.*)$`)

// codexLimitLines: only the terminal's last lines count; the message scrolls away once Codex
// runs again.
const codexLimitLines = 6

func codexLimitText(logInfo map[string]any) string {
	lines, _ := logInfo["recent_lines"].([]string)
	if len(lines) > codexLimitLines {
		lines = lines[len(lines)-codexLimitLines:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if m := codexLimitRe.FindStringSubmatch(lines[i]); m != nil {
			return capCLIText(strings.TrimSpace(m[1]))
		}
	}
	return ""
}

// maxCLIText caps a message taken from a CLI's transcript or terminal before the dashboard
// shows it: these are a line or two, and a transcript is the agent's to write.
const maxCLIText = 300

func capCLIText(text string) string {
	if len(text) > maxCLIText {
		text = strings.ToValidUTF8(text[:maxCLIText], "")
	}
	return text
}

func signedInSince(events []store.AgentEvent, t time.Time) bool {
	evs := make([]sessionstate.Event, 0, len(events))
	for _, e := range events {
		evs = append(evs, e.StateEvent())
	}
	return sessionstate.SignedInSince(evs, t)
}
