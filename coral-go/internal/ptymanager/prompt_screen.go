package ptymanager

import (
	"context"
	"github.com/subgentic/coral/internal/terminalinput"
	"regexp"
	"strings"
	"unicode/utf8"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/vtscreen"
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

// markedOptionRe: a line starting with a selection marker and text ("❯ No, exit",
// "> Yes, I trust this folder"); its text column is the end of group 1.
var markedOptionRe = regexp.MustCompile(`^(\s*[❯›>]\s+)\S`)

// menuNavRe: a menu footer menuFooter doesn't cover (agy: "↑/↓ Navigate · enter Confirm").
var menuNavRe = regexp.MustCompile(`(?i)↑/↓ navigate|enter confirm`)

// unnumberedMenu reports a menu whose options carry no numbers (Claude's and agy's
// trust screens, #3484's corpus): the last marked line, unnumbered, with no input
// box drawn below it, an unmarked option aligned with its text just above or below
// it, and a menu footer under it with no rule in between. An input box (even a
// wrapped draft) is closed by a rule before its footer, so it never matches.
func unnumberedMenu(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		m := markedOptionRe.FindStringSubmatchIndex(lines[i])
		if m == nil {
			continue
		}
		if menuOptionRe.MatchString(lines[i]) {
			return false // numbered: PromptOnScreen's own menu rule
		}
		for _, l := range lines[i+1:] {
			if isInputBoxLine(l) {
				return false // an input box drawn below: the menu was answered
			}
		}
		col := utf8.RuneCountInString(lines[i][:m[3]])
		sibling := false
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(lines) {
				continue
			}
			l := lines[j]
			t := strings.TrimSpace(l)
			if t == "" || isRuleLine(t) || markedOptionRe.MatchString(l) {
				continue
			}
			if indent(l) == col {
				sibling = true
			}
		}
		if !sibling {
			return false
		}
		for _, l := range lines[i+1:] {
			t := strings.TrimSpace(l)
			if isRuleLine(t) {
				return false
			}
			if promptFooterRe.MatchString(t) || menuNavRe.MatchString(t) {
				return true
			}
		}
		return false
	}
	return false
}

// isRuleLine: a horizontal rule drawn around an input box.
func isRuleLine(t string) bool {
	return strings.HasPrefix(t, "─") || strings.HasPrefix(t, "━") || strings.HasPrefix(t, "╰") || strings.HasPrefix(t, "╭")
}

// sgrStyleRe: a Select Graphic Rendition sequence (colour, bold, faint...).
var sgrStyleRe = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// screenText is a screen's text for the detectors. Styling (SGR) is removed with no
// trace: a PTY's raw output styles single characters (agy's shimmer draws
// "⣷  Running command..." one coloured letter at a time), and a space for each would
// break the words the detectors read (#3484, QA). Other escapes, cursor moves among
// them, still count as a space, as logparse.StripANSI has them.
func screenText(screen string) string {
	return logparse.StripANSI(sgrStyleRe.ReplaceAllString(screen, ""))
}

// footerWhy starts PromptOnScreen's reason when only a prompt's footer was seen.
const footerWhy = "prompt footer: "

// busyWhy starts PromptOnScreen's reason when the footer is drawn while the agent
// works (a spinner line on screen; #3472).
const busyWhy = "agent busy: "

// busyLineRe: a working agent's spinner line, as Antigravity draws it above its input
// box: a braille spinner and an activity ending in an ellipsis ("⣷  Working...",
// "⠋  Generating...", "⠙  Running command..."), a timer after it allowed.
// A stopgap from screen text (#3472): agy has no structured busy signal yet, and
// #3475 replaces screen reading with the CLIs' own signals where they exist.
var busyLineRe = regexp.MustCompile(`^\s*[\x{2800}-\x{28FF}]\s+\S.*(\.\.\.|…)`)

// busyOnScreen reports whether lines show a working agent's spinner line.
func busyOnScreen(lines []string) bool {
	for _, l := range lines {
		if busyLineRe.MatchString(l) {
			return true
		}
	}
	return false
}

// codexWorkingRe: Codex's working line above its input box, "• Working (12s • esc to interrupt)" (or a reasoning title in
// place of "Working": "• Planning the change (1m 3s • esc to interrupt)").
var codexWorkingRe = regexp.MustCompile(`(?i)^\s*[•◦]\s+\S.*\(.*\besc to interrupt\)\s*$`)

// codexWorkingLines: how many lines above Codex's input box are read for its working line (queued messages sit between them).
const codexWorkingLines = 6

// codexWorkingOnScreen reports whether Codex's screen shows it working (#247): its working line within the few lines just above
// the lowest input box. Only there: the same words further up are output (a reply quoting them), not the status.
func codexWorkingOnScreen(screen string) (line string, busy bool) {
	lines := tailLines(screenText(screen), promptScreenLines)
	box := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			box = i
			break
		}
	}
	if box < 0 {
		return "", false
	}
	seen := 0
	for i := box - 1; i >= 0 && seen < codexWorkingLines; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		seen++
		if codexWorkingRe.MatchString(t) {
			return t, true
		}
	}
	return "", false
}

// isPromptHold reports whether a hold's reason (PromptOnScreen's, or the agent's
// state) is a request from the agent to the user: a selection menu, a prompt
// question, or a state of needs input. A footer alone ("esc to cancel", which agy
// also shows while it runs a command), or one drawn while the agent works, holds
// sends, the safe side, but isn't reported as one (#3468, #3472).
func isPromptHold(why string) bool {
	return !strings.HasPrefix(why, footerWhy) && !strings.HasPrefix(why, busyWhy)
}

// PromptOnScreen reports whether screen, the visible text of an agent's
// terminal (ANSI sequences are stripped), ends in an open prompt, and why.
// A numbered list in an agent's reply is not a prompt: a menu has at least two
// options, one of them marked as selected, at the bottom of the screen.
func PromptOnScreen(screen string) (bool, string) {
	lines := tailLines(screenText(screen), promptScreenLines)
	all := lines
	if choices := dialogAboveInputBox(all); choices != "" {
		return true, dialogWhy + "a dialog is open above the input box (" + choices + ")"
	}
	if agySurveyOnlyOnScreen(screen) {
		// Antigravity's feedback survey takes a digit key too (#5229): held as a dialog, and dismissed like Claude's.
		return true, dialogWhy + "a dialog is open above the input box (" + agySurveyChoices + ")"
	}
	if unnumberedMenu(all) {
		return true, "selection menu"
	}
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
			if busyOnScreen(all) {
				// Antigravity draws "esc to cancel" under its input box while it works
				// (#3472): still held, the safe side, but it is busy, not asking.
				return true, busyWhy + t
			}
			return true, footerWhy + t
		}
		if promptQuestionRe.MatchString(t) && options >= 1 {
			return true, "prompt question: " + t
		}
	}
	// Claude's OAuth step waits for a pasted code with no menu: a send would be typed into it
	// (#3830).
	if on, why := ClaudeLoginOnScreen(screen); on {
		return true, loginWhy + why
	}
	return false, ""
}

// dialogWhy starts PromptOnScreen's reason for a dialog Claude Code draws directly above its input
// box (#4104): its bug-report draft ("1 to review · 2 to send · 0 to dismiss") or its session
// survey ("1: Bad  2: Fine  3: Good  0: Dismiss"). They act on single digit keys: a send that
// starts with a digit could answer one ("2 to send" sends a report), and while one is up an
// Enter doesn't submit the input box. Nothing is pasted or pressed until it closes.
const dialogWhy = "dialog: "

// isDialogHold reports whether a PromptOnScreen reason is a dialog (dialogWhy).
func isDialogHold(why string) bool { return strings.HasPrefix(why, dialogWhy) }

// dialogChoiceRe is one numbered choice of such a dialog: "2 to send", "2: Fine".
var dialogChoiceRe = regexp.MustCompile(`(?:^|[\s·│])(\d)(?::\s*| to )([A-Za-z]+)`)

// dialogChoices reports whether t offers at least two numbered choices, one of them to dismiss:
// both of Claude Code's digit-key dialogs do, and an agent's own numbered list doesn't.
func dialogChoices(t string) bool {
	m := dialogChoiceRe.FindAllStringSubmatch(t, -1)
	if len(m) < 2 {
		return false
	}
	for _, c := range m {
		if strings.EqualFold(c[2], "dismiss") {
			return true
		}
	}
	return false
}

// dialogAboveInputBox returns the choices line of a digit-key dialog drawn directly above the
// agent's input box, or "": a bordered box (╭ … ╰) holding the choices, or the choices line
// itself under a "●" question line, with only blank lines and the input box's own top rule between
// it and the input line.
// The same words anywhere else (an agent quoting them, a dialog higher up in the scrollback)
// don't count (the Orchestrator's #4104 condition).
func dialogAboveInputBox(lines []string) string {
	in := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			in = i
			break
		}
	}
	if in < 0 {
		return ""
	}
	i := in - 1
	for skipped := 0; i >= 0 && skipped < 4; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "─") || strings.HasPrefix(t, "━") {
			skipped++
			continue
		}
		break
	}
	if i < 0 {
		return ""
	}
	t := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(t, "╰") {
		// The survey: its choices line right above the box, under the question Claude Code draws
		// with its own "●" bullet. An agent's reply (its "⏺" bullet, or a line of one) quoting
		// the same words is not it.
		if i > 0 && dialogChoices(t) && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "●") {
			return t
		}
		return ""
	}
	for j := i - 1; j >= 0 && j >= i-12; j-- {
		t := strings.TrimSpace(lines[j])
		if !strings.HasPrefix(t, "│") {
			return "" // the box's top, or not a box: no choices inside it
		}
		if dialogChoices(t) {
			return strings.TrimSpace(strings.Trim(t, "│"))
		}
	}
	return ""
}

// loginWhy starts PromptOnScreen's reason for one of Claude's sign-in screens.
const loginWhy = "login screen: "

// tailLines returns the last n non-blank-trailing lines of s.
func tailLines(s string, n int) []string {
	// Claude draws a no-break space after its markers ("❯\u00a0text"); regexp's \s
	// is ASCII only, so without this its input box was never recognised and a
	// launch's first prompt waited the whole fallback (#3457, a real Claude 2.1.299
	// screen).
	s = strings.ReplaceAll(s, "\u00a0", " ")
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
	for _, line := range tailLines(screenText(screen), 8) {
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
	case at.Claude, at.Codex, at.Gemini, at.Agy, at.Antigravity:
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

// AgentScreenStyled implements StyledScreenReader for tmux: AgentScreen with each
// cell's style (#3468).
func (t *TmuxSessionTerminal) AgentScreenStyled(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return t.client.CapturePaneStyled(ctx, name, 40, agentType, sessionID)
}

// AgentScreen implements ScreenReader for the PTY backend: the session's output rendered
// as the screen (#4543). An empty render stays empty: the gate holds on a screen with no
// input box rather than reading box text from raw output it can't place.
func (p *PTYSessionTerminal) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	if _, err := p.keyFor(name, sessionID); err != nil {
		return "", err
	}
	return p.RenderedScreen(ctx, name, agentType, sessionID)
}

// RenderedScreenText is the session's output rendered at the size its PTY was last given
// (what PTYSessionTerminal.AgentScreen reads), for ScreenReaders built on a PTYBackend
// outside this package.
func (m *PTYBackend) RenderedScreenText(name string) (string, error) {
	data, err := m.Replay(name)
	if err != nil {
		return "", err
	}
	cols, rows, err := m.PaneSize(name)
	if err != nil {
		return "", err
	}
	return RenderScreen(data, cols, rows), nil
}

func (m *PTYBackend) renderedScreenStyled(name string) (string, error) {
	data, err := m.Replay(name)
	if err != nil {
		return "", err
	}
	cols, rows, err := m.PaneSize(name)
	if err != nil {
		return "", err
	}
	return vtscreen.RenderStyled(data, cols, rows), nil
}

// RenderedScreenStyledText is RenderedScreenText with faint cells marked (see AgentScreenStyled).
func (m *PTYBackend) RenderedScreenStyledText(name string) (string, error) {
	return m.renderedScreenStyled(name)
}

// ReplayScreen is the end of a PTY session's raw output: a fallback for a screen that
// renders empty. Gate checks use RenderedScreenText.
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
	return t.client.SendRawKeys(ctx, terminalinput.Internal, name, []string{"Enter"}, agentType, sessionID)
}

// SendArrow implements ArrowSender for tmux: its key names, which it sends in the pane's cursor-key mode.
func (t *TmuxSessionTerminal) SendArrow(ctx context.Context, name, agentType, sessionID string, up bool) error {
	key := "Down"
	if up {
		key = "Up"
	}
	return t.client.SendRawKeys(ctx, terminalinput.Internal, name, []string{key}, agentType, sessionID)
}
