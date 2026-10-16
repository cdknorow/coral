package routes

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// An agent that needs the user to sign in (#3830). The CLI's own record says so first: Claude
// Code writes an API error into its transcript when a turn fails (authentication_failed,
// "Invalid API key · Please run /login", an expired or revoked OAuth token). Before the
// transcript exists (a first run, waiting at the login screen) only the screen shows it, so the
// screen is read then and only then (#3475: CLI signals over screen parsing).

// loginScreenTTL: how long a screen read stays good. The list, the WebSocket feed, the gate and
// the chat poll all ask; the screen is read at most once per agent per TTL.
const loginScreenTTL = 3 * time.Second

// loginScreenTimeout bounds one screen read.
const loginScreenTimeout = 2 * time.Second

// loginScreens is the last screen reading per agent, for agents with no transcript yet.
type loginScreens struct {
	mu sync.Mutex
	m  map[string]loginScreenRead
}

type loginScreenRead struct {
	at  time.Time
	on  bool
	why string
}

func (l *loginScreens) read(key string, now time.Time, fn func() (bool, string)) (bool, string) {
	l.mu.Lock()
	if r, ok := l.m[key]; ok && now.Sub(r.at) < loginScreenTTL {
		l.mu.Unlock()
		return r.on, r.why
	}
	l.mu.Unlock()
	on, why := fn()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]loginScreenRead{}
	}
	if len(l.m) >= 256 {
		for k, r := range l.m {
			if now.Sub(r.at) >= loginScreenTTL {
				delete(l.m, k)
			}
		}
	}
	l.m[key] = loginScreenRead{at: now, on: on, why: why}
	return on, why
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
		return true, apiErr.Text
	}
	reader, ok := h.terminal.(ptymanager.ScreenReader)
	if !ok || name == "" {
		return false, ""
	}
	on, _ := h.loginScreens.read(promptReadKey(sessionID, name, at.Claude), time.Now(), func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), loginScreenTimeout)
		defer cancel()
		screen, err := reader.AgentScreen(ctx, name, at.Claude, sessionID)
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
			return claudeLimitText(apiErr.Text)
		}
	case at.Codex:
		return codexLimitText(logInfo)
	}
	return ""
}

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
			text := strings.TrimSpace(m[1])
			if len(text) > 300 {
				text = strings.ToValidUTF8(text[:300], "")
			}
			return text
		}
	}
	return ""
}

func signedInSince(events []store.AgentEvent, t time.Time) bool {
	evs := make([]sessionstate.Event, 0, len(events))
	for _, e := range events {
		evs = append(evs, e.StateEvent())
	}
	return sessionstate.SignedInSince(evs, t)
}
