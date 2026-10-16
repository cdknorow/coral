package ptymanager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/cdknorow/coral/internal/inputlog"
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

// boxScanLines bounds how far up the screen inputBoxText looks for the input box: the whole
// pane (#3708). It looked at the last 12 lines, and a long automated paste wraps over more than
// that, so its box's first line was off the window: the paste read as "didn't reach the input
// box" though it had, and every later send waited behind it.
const boxScanLines = 400

// boxStartLine is the index of the input box's first line in lines, or -1: the lowest line that
// starts like one, then up through that same box (to its border or a blank line) to the topmost
// such line, since a line of pasted text can itself start with "> " (a quote).
func boxStartLine(lines []string) int {
	low := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			low = i
			break
		}
	}
	start := low
	for i := low - 1; i >= 0 && low >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || isBoxBorder(t) {
			break
		}
		if isInputBoxLine(lines[i]) {
			start = i
		}
	}
	return start
}

// isBoxBorder: a horizontal rule or box corner that bounds an agent's input box.
func isBoxBorder(t string) bool {
	return strings.HasPrefix(t, "─") || strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "╰") || strings.HasPrefix(t, "━")
}

// inputBoxText returns what the agent's input box holds (its line after the marker
// and the wrapped lines under it), or ok=false when no input box is on screen.
func inputBoxText(screen string) (text string, ok bool) {
	if open, _ := PromptOnScreen(screen); open {
		return "", false
	}
	lines := tailLines(screenText(screen), boxScanLines)
	start := boxStartLine(lines)
	if start < 0 {
		return "", false
	}
	m := inputBoxRe.FindStringSubmatchIndex(lines[start])
	parts := []string{lines[start][m[1]:]}
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l)
		if t == "" || isBoxBorder(t) {
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

// StyledScreenReader is implemented by terminals that can show AgentScreen with each
// cell's style as SGR sequences (tmux's capture-pane -e).
type StyledScreenReader interface {
	AgentScreenStyled(ctx context.Context, name, agentType, sessionID string) (string, error)
}

// suggestionOnly reports whether the input box on a styled screen (capture-pane -e)
// holds only faint text: after a turn Claude shows a suggestion for the next prompt
// in the empty box, drawn faint (SGR 2), e.g. "❯ coral-board read" (#3468). Text a
// person typed, or a suggestion they accepted with Tab, is drawn at normal
// intensity, so one normal character anywhere in the box (a draft followed by faint
// ghost text) means it is someone else's text. A faint cell is only ever Claude's
// own rendering: what is typed or pasted into the box is never drawn faint.
func suggestionOnly(styled string) bool {
	lines := styledLines(styled)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].text) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > boxScanLines {
		lines = lines[len(lines)-boxScanLines:]
	}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	start := boxStartLine(texts)
	if start < 0 {
		return false
	}
	box := []styledLine{lines[start]}
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l.text)
		if t == "" || strings.HasPrefix(t, "─") || strings.HasPrefix(t, "╰") || strings.HasPrefix(t, "━") {
			break
		}
		box = append(box, l)
	}
	if strings.Contains(squeeze(box[0].text)+squeeze(joinText(box[1:])), pastePlaceholder) {
		return false // a paste placeholder is never a suggestion
	}
	faint := 0
	for i, l := range box {
		from := 0
		if i == 0 {
			from = inputBoxRe.FindStringSubmatchIndex(l.text)[1]
		}
		for _, c := range l.cells {
			if c.at < from || unicode.IsSpace(c.r) || c.r == '│' || c.r == '┃' || c.r == '|' {
				continue
			}
			if !c.faint {
				return false
			}
			faint++
		}
	}
	return faint > 0
}

type styledCell struct {
	r     rune
	faint bool
	at    int // byte offset in the line's text
}

type styledLine struct {
	text  string // the line without escape sequences (U+00A0 as a space)
	cells []styledCell
}

func joinText(ls []styledLine) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(l.text)
	}
	return b.String()
}

// styledLines splits a styled capture into lines of cells, tracking SGR 2 (faint)
// across lines as the terminal does; every other escape sequence is dropped.
func styledLines(s string) []styledLine {
	var out []styledLine
	var cur styledLine
	var text strings.Builder
	faint := false
	rs := []rune(strings.ReplaceAll(s, "\r", ""))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\n':
			cur.text = text.String()
			out = append(out, cur)
			cur, text = styledLine{}, strings.Builder{}
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] == 'm' {
				faint = sgrFaint(string(rs[i+2:j]), faint)
			}
			i = j
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == ']': // OSC, to BEL or ST
			j := i + 2
			for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
				j++
			}
			if j < len(rs) && rs[j] == 0x1b {
				j++
			}
			i = j
		case r == 0x1b:
			i++ // a two-character escape
		case r < 0x20 || r == 0x7f:
		default:
			if r == '\u00a0' {
				r = ' '
			}
			cur.cells = append(cur.cells, styledCell{r: r, faint: faint, at: text.Len()})
			text.WriteRune(r)
		}
	}
	cur.text = text.String()
	return append(out, cur)
}

// sgrFaint applies one SGR sequence's parameters to the faint attribute.
func sgrFaint(params string, faint bool) bool {
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		p := ps[i]
		if j := strings.IndexByte(p, ':'); j >= 0 { // a colon sub-parameter (38:5:n)
			continue
		}
		switch p {
		case "", "0", "22":
			faint = false
		case "2":
			faint = true
		case "38", "48", "58": // a colour: 5;n or 2;r;g;b
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return faint
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

// A held paste of Coral's own that can't be confirmed (#3708). The gate pasted the text, then
// the input box never showed it, couldn't be read, or showed text Coral can't place. Held as it
// was, the send waited forever and every later send waited behind it (the coral-hub
// Orchestrator got no board message for half an hour). After pasteStuckAfter (sooner with a
// mention behind it) it is given up: dropped with a logged error and a notice on the agent,
// saying the text may still be in its box. Enter is never pressed on a box Coral can't read or
// whose text isn't Coral's: that could submit a person's draft.
var (
	pasteStuckAfter       = time.Minute
	pasteStuckAfterUrgent = 10 * time.Second
)

// errPasteGaveUp is the error of a held paste the gate gave up (the next held send goes).
var errPasteGaveUp = errors.New("a held paste was given up: its input box never confirmed it")

type mentionWaitingKey struct{}

// mentionWaiting reports whether a mention is being delivered behind what is flushed.
func mentionWaiting(ctx context.Context) bool {
	v, _ := ctx.Value(mentionWaitingKey{}).(bool)
	return v
}

// pasteStuck reports whether a held paste of Coral's has waited long enough to give up.
func (g *inputGate) pasteStuck(in AutomatedInput) bool {
	if in.heldSince.IsZero() {
		return false
	}
	limit := pasteStuckAfter
	if in.urgent {
		limit = pasteStuckAfterUrgent
	}
	return g.now().Sub(in.heldSince) >= limit
}

// giveUpPaste drops a stuck held paste: logged, recorded in the input log, and reported to
// HeldGaveUpFunc. It returns errPasteGaveUp.
func (g *inputGate) giveUpPaste(in AutomatedInput, why string) error {
	agentType, sessionID := in.identity()
	slog.Warn("automated input: gave up a held paste", "session", in.Name, "sender", in.What, "why", why,
		"held_for", g.now().Sub(in.heldSince).Round(time.Second).String())
	inputlog.Record(in.Name, inputlog.Text, "[GAVE UP, "+why+"] "+in.Text)
	heldGaveUp.RLock()
	fn := heldGaveUp.fn
	heldGaveUp.RUnlock()
	if fn != nil {
		name, what := in.Name, in.What
		g.notes.post(func() { fn(name, agentType, sessionID, what, why) })
	}
	return errPasteGaveUp
}
