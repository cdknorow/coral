package ptymanager

import (
	"bytes"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/naming"
)

// Automated text as a bracketed paste (#3457). Measured with Claude 2.1.299, Codex
// 0.155.1 and agy: a menu (a permission prompt, an update or trust question) ignores a
// bracketed paste, digits, CR and LF included, where a typed "4" picks option 4. So
// the gate pastes automated text instead of typing it, and a prompt that opens just
// before the paste can't take it as an answer. An ignored paste is dropped, not
// queued, so the gate checks that the text reached the input box before its Enter,
// and holds the send (to paste again) when it didn't.

// RenderedScreen is implemented by terminals whose AgentScreen is the rendered pane
// (tmux), not raw output (a PTY's replay), so the input box's contents can be read
// from it.
type RenderedScreen interface {
	RenderedScreen() bool
}

func (t *TmuxSessionTerminal) RenderedScreen() bool { return true }

// pastePrefixRunes is how much of the text identifies it in the input box: enough to
// tell it from a placeholder, short enough to be on screen however the box wraps.
const pastePrefixRunes = 24

// squeeze drops all white space, so text wrapped across the box's lines compares
// equal to the text pasted.
func squeeze(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsSpace(r) && r != '│' && r != '┃' && r != '|' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// inputBoxText returns what the agent's input box holds (its line after the marker
// and the wrapped lines under it), or ok=false when no input box is on screen.
func inputBoxText(screen string) (text string, ok bool) {
	if open, _ := PromptOnScreen(screen); open {
		return "", false
	}
	lines := tailLines(logparse.StripANSI(screen), 12)
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	m := inputBoxRe.FindStringSubmatchIndex(lines[start])
	parts := []string{lines[start][m[1]:]}
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "─") || strings.HasPrefix(t, "╰") || strings.HasPrefix(t, "━") {
			break
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " "), true
}

// boxState is what an agent's input box holds, for a send's text.
type boxState int

const (
	boxUnknown boxState = iota // no input box on screen (a prompt, or output in the way)
	boxEmpty                   // empty, or the agent's placeholder
	boxOurs                    // the text (its start), or the placeholder for a long paste
	boxOther                   // something else: a person's draft, or text Coral can't place
)

// inputBoxState reads screen's input box for text. hadPlaceholder says the box held
// a "[Pasted ...]" placeholder before Coral pasted: then one is never taken as ours,
// it is a person's paste (#3457 v3).
func inputBoxState(screen, text string, hadPlaceholder bool) boxState {
	box, ok := inputBoxText(screen)
	if !ok {
		return boxUnknown
	}
	if strings.Contains(box, pastePlaceholder) {
		if !hadPlaceholder && collapsible(text) {
			return boxOurs
		}
		return boxOther
	}
	want := []rune(squeeze(text))
	if len(want) > pastePrefixRunes {
		want = want[:pastePrefixRunes]
	}
	if len(want) > 0 && strings.Contains(squeeze(box), string(want)) {
		return boxOurs
	}
	if boxPlaceholder(box) {
		return boxEmpty
	}
	return boxOther
}

// pastePlaceholder starts what an agent shows instead of a long paste
// ("[Pasted text #1 +12 lines]").
const pastePlaceholder = "[Pasted"

// collapsible: text an agent might show as a placeholder rather than as itself (more
// than one line, or long). A one-line nudge never is, so a placeholder can't be it.
func collapsible(text string) bool {
	return strings.Contains(strings.TrimRight(text, "\n"), "\n") || len(text) > 400
}

// hasPlaceholder reports whether screen's input box shows a paste placeholder.
func hasPlaceholder(screen string) bool {
	box, ok := inputBoxText(screen)
	return ok && strings.Contains(box, pastePlaceholder)
}

// boxPlaceholder: an empty box, or the hint an agent shows in it (seen on Claude
// 2.1.299: Try "how do I log an error?"; Codex 0.155.1: Ask Codex to do anything;
// Gemini CLI: Type your message or @path/to/file).
func boxPlaceholder(box string) bool {
	t := strings.TrimSpace(box)
	return t == "" || strings.HasPrefix(t, `Try "`) || strings.HasPrefix(t, "Ask Codex to do anything") ||
		strings.HasPrefix(t, "Type your message")
}

// pasteLanded reports whether screen's input box shows text (no placeholder before).
func pasteLanded(screen, text string) bool { return inputBoxState(screen, text, false) == boxOurs }

// BracketedPasteFromLog reads an agent's bracketed paste mode from its pane's output
// log (tmux panes can't be asked): the last DECSET or DECRST 2004 it printed.
// known is false when the log is missing or never set the mode.
func BracketedPasteFromLog(logDir, agentType, sessionID string) (enabled, known bool) {
	if logDir == "" || agentType == "" || sessionID == "" {
		return false, false
	}
	f, err := os.Open(naming.LogFile(logDir, agentType, sessionID))
	if err != nil {
		return false, false
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(st.Size()-tail, io.SeekStart); err != nil {
			return false, false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, tail))
	if err != nil {
		return false, false
	}
	on := bytes.LastIndex(data, []byte("\x1b[?2004h"))
	off := bytes.LastIndex(data, []byte("\x1b[?2004l"))
	switch {
	case on > off:
		return true, true
	case off > on:
		return false, true
	}
	return false, false
}

// identity is the agent type and session id of a send, from its session name when
// the caller didn't give them ("claude-<id>").
func (in AutomatedInput) identity() (agentType, sessionID string) {
	agentType, sessionID = in.AgentType, in.SessionID
	if agentType == "" && sessionID == "" {
		if i := strings.IndexByte(in.Name, '-'); i > 0 {
			agentType, sessionID = in.Name[:i], naming.SessionIDFromName(in.Name)
		}
	}
	return agentType, sessionID
}
