package ptymanager

import (
	"context"
	"regexp"
	"strings"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/logparse"
)

// Recognising, from the bottom of an agent's screen, that it is waiting on a
// prompt from the user (#3431): a permission or selection menu, an approval
// question, a trust-this-folder or login/model picker. Automated text typed
// there followed by Enter would answer it, so the automated-input gate holds
// it instead. The rules lean towards "waiting": holding a notification a few
// seconds too long is harmless, answering a prompt is not.

// promptScreenLines is how far up from the bottom a prompt is looked for: a
// menu's option list plus its footer ("Enter to select · Esc to cancel").
const promptScreenLines = 14

var (
	// An option line of a numbered menu: Claude marks the highlighted option
	// with ❯, Codex and agy with › or >.
	menuOptionRe = regexp.MustCompile(`^\s*([❯›>]\s*)?(\d{1,2})[.)]\s+\S`)
	// Questions that only appear while a prompt is open.
	promptQuestionRe = regexp.MustCompile(`(?i)^\s*(do you want to\b|would you like to\b|do you trust the files\b|allow\b.*\?\s*$)`)
	// Footers drawn under an open menu or approval prompt.
	promptFooterRe = regexp.MustCompile(`(?i)(enter to (select|confirm|submit)|esc to (cancel|go back|reject|interrupt and reject)|↑/↓ to (navigate|select)|press enter to (confirm|continue))`)
)

// PromptOnScreen reports whether screen, the visible text of an agent's
// terminal (ANSI sequences are stripped), ends in an open prompt, and why.
// A numbered list in an agent's reply is not a prompt: a menu has at least two
// options, one of them marked as selected, at the bottom of the screen.
func PromptOnScreen(screen string) (bool, string) {
	lines := tailLines(logparse.StripANSI(screen), promptScreenLines)
	// An open menu or approval replaces the agent's input box. One drawn below
	// a menu means the menu was answered (a terminal that didn't clear it still
	// shows it above): only what is below the last input box counts.
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			lines = lines[i+1:]
			break
		}
	}
	options, selected := 0, false
	for _, line := range lines {
		if m := menuOptionRe.FindStringSubmatch(line); m != nil && indent(line) < 5 {
			options++
			if m[1] != "" {
				selected = true
			}
		}
	}
	if options >= 2 && selected {
		return true, "selection menu"
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if promptFooterRe.MatchString(t) {
			return true, "prompt footer: " + t
		}
		if promptQuestionRe.MatchString(t) && options >= 1 {
			return true, "prompt question: " + t
		}
	}
	return false, ""
}

// tailLines returns the last n non-blank-trailing lines of s.
func tailLines(s string, n int) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func indent(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// inputBoxRe matches an agent's input line: Claude's "> " or "❯ " (inside its
// box's "│"), Codex's "› " or "▌", Gemini's and agy's "> ".
var inputBoxRe = regexp.MustCompile(`^\s*[│|┃]?\s*([>❯›▌])(\s|$)`)

// InputBoxOnScreen reports whether the bottom of screen shows the agent's
// normal input box with no prompt open: it is ready for typed text.
func InputBoxOnScreen(screen string) bool {
	if open, _ := PromptOnScreen(screen); open {
		return false
	}
	for _, line := range tailLines(logparse.StripANSI(screen), 8) {
		if isInputBoxLine(line) {
			return true
		}
	}
	return false
}

// isInputBoxLine reports whether line is an agent's input line (not a menu's
// highlighted option, which starts with the same marker).
func isInputBoxLine(line string) bool {
	return inputBoxRe.MatchString(line) && !menuOptionRe.MatchString(strings.TrimLeft(line, "│|┃ "))
}

// InputBoxKnown reports whether InputBoxOnScreen recognises agentType's input
// box. For other agents a launch's first prompt waits only for no prompt to be
// open (a box Coral can't recognise would hold it forever).
func InputBoxKnown(agentType string) bool {
	switch agentType {
	case at.Claude, at.Codex, at.Gemini:
		return true
	}
	return false
}

// screenTailBytes is how much of a PTY's output is read for its screen: enough
// for a menu and its footer.
const screenTailBytes = 16 << 10

// screenTail returns the end of raw terminal output, for PromptOnScreen.
func screenTail(b []byte) string {
	if len(b) > screenTailBytes {
		b = b[len(b)-screenTailBytes:]
	}
	return string(b)
}

// AgentScreen implements ScreenReader for tmux: the bottom of the agent's pane.
func (t *TmuxSessionTerminal) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return t.client.CapturePane(ctx, name, 40, agentType, sessionID)
}

// AgentScreen implements ScreenReader for the PTY backend: the end of the
// session's output.
func (p *PTYSessionTerminal) AgentScreen(_ context.Context, name, _, sessionID string) (string, error) {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return "", err
	}
	data, err := p.backend.Replay(key)
	if err != nil {
		return "", err
	}
	return screenTail(data), nil
}

// ReplayScreen is the end of a PTY session's output, for ScreenReaders built on
// a PTYBackend outside this package.
func (m *PTYBackend) ReplayScreen(name string) (string, error) {
	data, err := m.Replay(name)
	if err != nil {
		return "", err
	}
	return screenTail(data), nil
}

// SendEnter implements EnterSender for tmux: the Enter a send withheld because
// a prompt opened after its text (#3431). PTY sends write text and Enter in one
// write, so they never withhold it.
func (t *TmuxSessionTerminal) SendEnter(ctx context.Context, name, agentType, sessionID string) error {
	return t.client.SendRawKeys(ctx, name, []string{"Enter"}, agentType, sessionID)
}
