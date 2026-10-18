package routes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// startupPromptState is why an agent that has sent no event yet waits on a prompt its CLI drew at launch
// (ptymanager.StartupPromptOnScreen), with the prompt's question, or "" (#4543: Coral Hub's "Needs you" page
// and its alerts read waiting_for_input, and a Codex agent on its hook review or a Claude agent on its trust
// menu looked ready). Read only while the session has no event, through the same per-agent cache as the
// login screen: at most once per loginScreenTTL, or once while the terminal writes nothing.
func (h *SessionsHandler) startupPromptState(name, agentType, sessionID string) (string, string) {
	if sessionID == "" || name == "" {
		return "", ""
	}
	stamp := ""
	if stamper, _ := h.backend.(ptymanager.OutputStamper); stamper != nil {
		if st, ok := stamper.OutputStamp(agentType, sessionID); ok {
			stamp = st
		}
	}
	on, why := h.startupScreens.read(promptReadKey(sessionID, name, agentType), stamp, time.Now(), func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), loginScreenTimeout)
		defer cancel()
		screen, err := h.agentScreen(ctx, name, agentType, sessionID, stamp)
		if err != nil {
			return false, ""
		}
		reason := ptymanager.StartupPromptOnScreen(screen)
		if reason == "" {
			return false, ""
		}
		question := ""
		if s, ok := parsePromptScreen(screen); ok {
			question = s.Question
		}
		return true, reason + "\x00" + question
	})
	if !on {
		return "", ""
	}
	reason, question, _ := strings.Cut(why, "\x00")
	return reason, capCLIText(question)
}

// agentScreen is the agent's visible screen as text: the PTY's rendered (its output is not the screen as
// drawn), else, with an output stamp (tmux), the capture the poll shares, else a ScreenReader's. A terminal
// that is neither is not read (as for the login screen).
func (h *SessionsHandler) agentScreen(ctx context.Context, name, agentType, sessionID, stamp string) (string, error) {
	if rs, ok := h.terminal.(ptymanager.RenderedScreenReader); ok {
		return rs.RenderedScreen(ctx, name, agentType, sessionID)
	}
	if stamp != "" {
		return h.sharedCapture(ctx, name, 40, agentType, sessionID)
	}
	if reader, ok := h.terminal.(ptymanager.ScreenReader); ok {
		return reader.AgentScreen(ctx, name, agentType, sessionID)
	}
	return "", errNoScreen
}

var errNoScreen = errors.New("this terminal has no screen to read")
