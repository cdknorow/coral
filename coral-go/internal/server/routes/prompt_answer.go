package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// Answering an agent's prompt from the chat.
//
// Claude Code's permission prompts, AskUserQuestion dialogs and plan
// approvals draw a numbered option list at the bottom of the terminal, with
// "❯" marking the selected option; pressing an option's digit answers it.
// Codex draws its sign-in menu and approval prompts the same way, marking
// the selected option with ">" (or "›"), and takes the same digits
// (a multi-question dialog advances to the next question, then to a
// "Submit answers" review). The option list varies (permission choices
// depend on the tool and mode), so it is read from the screen rather than
// assumed, and an answer is only sent after re-reading the screen and
// confirming the digit still maps to the option the user clicked.

// How an option is answered:
//   - "select": its digit answers it
//   - "text":   its digit opens a text field ("Type something.", "Tell Claude
//     what to change"); the answer is typed, then Enter
//   - "chat":   its digit closes the dialog so the user can reply in chat
//     ("Chat about this")
type promptOption struct {
	N        int    `json:"n"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Action   string `json:"action"`
}

// One line of the "Review your answers" step of a multi-question dialog.
type promptReviewItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type promptScreen struct {
	Question string             `json:"question"`
	Options  []promptOption     `json:"options"`
	Review   []promptReviewItem `json:"review,omitempty"`
	// Unnumbered: the options carry no numbers (Claude Code's folder-trust and API-key menus at launch,
	// #4543); n is an option's place in the list, and it is answered with the arrow keys and Enter.
	Unnumbered bool `json:"unnumbered,omitempty"`
	// The few lines above the question (a permission prompt's command): with the question and the labels,
	// what tells this prompt from the next one the agent opens (samePrompt).
	context string
}

// Time for the dialog to turn an option into a text field before typing
var promptTextDelay = 300 * time.Millisecond

// After an answer is sent the screen is read again (#4543): the answer is ok only once the prompt it
// answered has gone (the next question, the review step, or the agent at work), and Codex's menus that only
// move their selection on a digit are confirmed with Enter.
var (
	promptPollEvery = 50 * time.Millisecond
	// How long the same prompt must stay up, with the chosen option selected, before Enter confirms it:
	// a menu that a digit answers redraws within a frame or two, and must not get the Enter as well.
	promptSettle = 250 * time.Millisecond
	// How long an answered prompt has to go.
	promptClearTimeout = 2500 * time.Millisecond
)

var (
	promptOptionRe = regexp.MustCompile(`^\s*([❯›>]\s*)?(\d{1,2})\.\s+(.+?)\s*$`)
	separatorRe    = regexp.MustCompile(`^\s*[─━╌┄═]+\s*$`)
)

func optionAction(label string) string {
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.HasPrefix(lower, "type something"),
		strings.HasPrefix(lower, "tell claude what to change"),
		strings.HasPrefix(lower, "tell agent what to change"),
		strings.HasPrefix(lower, "tell antigravity what to change"):
		return "text"
	case strings.HasPrefix(lower, "chat about this"):
		return "chat"
	}
	return "select"
}

func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// parsePromptScreen reads the option block of an open prompt from the bottom
// of a terminal capture. It walks up from the last option line through the
// options, their wrapped/description lines (indented five or more) and
// separators, and stops at the prompt's own question line. A numbered list
// elsewhere on screen (a plan's steps, a reply) is never reached, and a block
// without a selection marker is not a prompt.
func parsePromptScreen(capture string) (promptScreen, bool) {
	lines := strings.Split(strings.ReplaceAll(capture, "\r", ""), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	// The option list ends within a few lines of the bottom (a footer such
	// as "Enter to select · Esc to cancel" may follow it).
	end := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-6; i-- {
		if promptOptionRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return parseUnnumberedMenu(lines)
	}
	var opts []promptOption
	question := ""
	questionAt := -1
	for i := end; i >= 0; i-- {
		line := lines[i]
		if m := promptOptionRe.FindStringSubmatch(line); m != nil && indentOf(line) < 5 {
			n, _ := strconv.Atoi(m[2])
			label := m[3]
			opts = append([]promptOption{{N: n, Label: label, Selected: m[1] != "", Action: optionAction(label)}}, opts...)
			continue
		}
		if strings.TrimSpace(line) == "" || separatorRe.MatchString(line) || indentOf(line) >= 5 {
			continue
		}
		// Codex's update menu puts a release-notes link between its
		// "Update available!" heading and the options
		if strings.HasPrefix(strings.TrimSpace(line), "Release notes:") {
			continue
		}
		question = strings.TrimSpace(line)
		questionAt = i
		// A long question wraps onto lines above with the same indent; join
		// them. Header/tab rows, review items, blanks and rules end it, and a
		// different indent (a permission prompt's command above "Do you want
		// to proceed?") is not part of it.
		indent := indentOf(line)
		for j := i - 1; j >= 0 && j >= i-8; j-- {
			prev := lines[j]
			t := strings.TrimSpace(prev)
			if t == "" || separatorRe.MatchString(prev) || indentOf(prev) != indent || promptOptionRe.MatchString(prev) ||
				strings.ContainsAny(t[:min(len(t), 4)], "☐☒←●→│✔❯⎿⏺") || t == "Review your answers" {
				break
			}
			question = t + " " + question
			questionAt = j
		}
		break
	}
	if len(opts) < 2 {
		return promptScreen{}, false
	}
	selected := false
	for i, o := range opts {
		if o.N != i+1 {
			return promptScreen{}, false // not a clean 1..k list
		}
		selected = selected || o.Selected
	}
	if !selected {
		return promptScreen{}, false
	}
	return promptScreen{Question: question, Options: opts, Review: parseReview(lines, questionAt), context: promptContext(lines, questionAt)}, true
}

var (
	// A menu's footer: Claude Code's "Enter to confirm · Esc to cancel"
	menuFooterRe = regexp.MustCompile(`(?i)\benter to (confirm|select|continue)\b|\besc to cancel\b`)
	// The selected option of an unnumbered menu: its marker, then its label
	markedLineRe = regexp.MustCompile(`^(\s*)[❯›>]\s+(\S.*?)\s*$`)
)

// parseUnnumberedMenu reads a menu whose options carry no numbers from the bottom of a capture (lines, with
// trailing blank lines removed): a menu footer within the last three lines, and just above it one block of
// lines that are all options, one of them marked and the others aligned with its label. Its question is the
// nearest paragraph above with a "?" in it (Claude's trust menu has its "Security guide" link in between),
// else the nearest paragraph.
func parseUnnumberedMenu(lines []string) (promptScreen, bool) {
	footer := -1
	for i, seen := len(lines)-1, 0; i >= 0 && seen < 3; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		seen++
		if menuFooterRe.MatchString(t) {
			footer = i
			break
		}
	}
	if footer < 0 {
		return promptScreen{}, false
	}
	last := footer - 1
	for last >= 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	first := last
	for first > 0 && strings.TrimSpace(lines[first-1]) != "" {
		first--
	}
	if last < 0 || last-first < 1 {
		return promptScreen{}, false
	}
	marked, col := -1, -1
	for i := first; i <= last; i++ {
		if m := markedLineRe.FindStringSubmatchIndex(lines[i]); m != nil {
			if marked >= 0 {
				return promptScreen{}, false
			}
			marked, col = i, utf8.RuneCountInString(lines[i][:m[4]])
		}
	}
	if marked < 0 {
		return promptScreen{}, false
	}
	var opts []promptOption
	for i := first; i <= last; i++ {
		line := lines[i]
		label := strings.TrimSpace(line)
		if i == marked {
			label = markedLineRe.FindStringSubmatch(line)[2]
		} else if utf8.RuneCountInString(line)-utf8.RuneCountInString(strings.TrimLeft(line, " ")) != col || promptOptionRe.MatchString(line) {
			return promptScreen{}, false // not aligned with the marked option: not a list of options
		}
		label = strings.TrimSpace(strings.TrimPrefix(label, "✔")) // the current choice's check mark
		opts = append(opts, promptOption{N: len(opts) + 1, Label: label, Selected: i == marked, Action: "select"})
	}
	question, questionAt := "", -1
	for i := first - 1; i >= 0 && i >= first-16 && !separatorRe.MatchString(lines[i]); i-- {
		if strings.Contains(lines[i], "?") {
			question, questionAt = paragraphAt(lines, i)
			break
		}
	}
	if questionAt < 0 {
		for i := first - 1; i >= 0 && !separatorRe.MatchString(lines[i]); i-- {
			if strings.TrimSpace(lines[i]) != "" {
				question, questionAt = paragraphAt(lines, i)
				break
			}
		}
	}
	if questionAt < 0 {
		questionAt = first
	}
	return promptScreen{Question: question, Options: opts, Unnumbered: true, context: promptContext(lines, questionAt)}, true
}

// paragraphAt is the paragraph (non-blank lines between blank lines and rules) holding lines[i], joined, and
// the index of its first line.
func paragraphAt(lines []string, i int) (string, int) {
	in := func(j int) bool {
		return j >= 0 && j < len(lines) && strings.TrimSpace(lines[j]) != "" && !separatorRe.MatchString(lines[j])
	}
	start, end := i, i
	for in(start - 1) {
		start--
	}
	for in(end + 1) {
		end++
	}
	var parts []string
	for _, l := range lines[start : end+1] {
		parts = append(parts, strings.TrimSpace(l))
	}
	return strings.Join(parts, " "), start
}

// promptContext is up to three non-blank lines above the question, trimmed.
func promptContext(lines []string, questionAt int) string {
	var above []string
	for i := questionAt - 1; i >= 0 && len(above) < 3; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			above = append([]string{t}, above...)
		}
	}
	return strings.Join(above, "\n")
}

// samePrompt reports whether two reads show the same prompt, wherever its selection is.
func samePrompt(a, b promptScreen) bool {
	if a.Question != b.Question || a.context != b.context || len(a.Options) != len(b.Options) {
		return false
	}
	for i := range a.Options {
		if normalizeLabel(a.Options[i].Label) != normalizeLabel(b.Options[i].Label) {
			return false
		}
	}
	return true
}

// parseReview reads the "Review your answers" list shown above the Submit
// step of a multi-question dialog:
//
//	Review your answers
//	 ● What is your favorite fruit?
//	   → Pear
func parseReview(lines []string, questionAt int) []promptReviewItem {
	if questionAt <= 0 {
		return nil
	}
	start := -1
	for i := questionAt - 1; i >= 0 && i >= questionAt-40; i-- {
		if strings.TrimSpace(lines[i]) == "Review your answers" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var items []promptReviewItem
	for _, line := range lines[start+1 : questionAt] {
		// The focused item carries a bar ("│ ● ..."); strip such markers
		t := strings.TrimSpace(strings.TrimLeft(line, reviewMarkers))
		switch {
		case strings.HasPrefix(t, "●"):
			items = append(items, promptReviewItem{Question: strings.TrimSpace(strings.TrimPrefix(t, "●"))})
		case strings.HasPrefix(t, "→") && len(items) > 0:
			items[len(items)-1].Answer = strings.TrimSpace(strings.TrimPrefix(t, "→"))
		case t != "" && len(items) > 0:
			// A long question or answer wraps onto the next line
			last := &items[len(items)-1]
			if last.Answer != "" {
				last.Answer += " " + t
			} else {
				last.Question += " " + t
			}
		}
	}
	return items
}

// Characters Claude Code draws before a review item (focus bar, indent)
const reviewMarkers = " \t│┃▎▍▌|>"

func (h *SessionsHandler) capturePromptScreen(ctx context.Context, name, agentType, sessionID string) (promptScreen, bool) {
	s, ok, err := h.readPromptScreen(ctx, name, agentType, sessionID)
	return s, ok && err == nil
}

// readPromptScreen reads the prompt open on the agent's screen; an error is a screen that couldn't be read,
// which is not a screen without a prompt.
func (h *SessionsHandler) readPromptScreen(ctx context.Context, name, agentType, sessionID string) (promptScreen, bool, error) {
	var text string
	var err error
	if rs, ok := h.terminal.(ptymanager.RenderedScreenReader); ok {
		text, err = rs.RenderedScreen(ctx, name, agentType, sessionID) // the PTY's output is not the screen as drawn (#4543)
	} else {
		text, err = h.terminal.CaptureOutput(ctx, name, 80, agentType, sessionID)
	}
	if err != nil {
		return promptScreen{}, false, err
	}
	s, ok := parsePromptScreen(text)
	return s, ok, nil
}

func normalizeLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// AnswerPrompt selects an option of the prompt open in the agent's terminal.
// The screen is re-read first: the digit is sent only if that option still
// has the label the user clicked, so a prompt that changed or closed is
// never answered by mistake.
// POST /api/sessions/live/{name}/answer-prompt {"session_id","agent_type","n","label"}
func (h *SessionsHandler) AnswerPrompt(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID string `json:"session_id"`
		AgentType string `json:"agent_type"`
		N         int    `json:"n"`
		Label     string `json:"label"`
		Text      string `json:"text"` // the typed answer, for a "text" option
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.N < 1 || body.N > 9 || strings.TrimSpace(body.Label) == "" {
		errBadRequest(w, "n (1-9) and label are required")
		return
	}
	// An answer always names its exact target, as for Send: a bare name would reach
	// whatever pane matches it and could answer another agent's prompt (#3211).
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}
	// The agent's own terminal is gone: say so, rather than "the prompt changed" (#3211)
	if body.SessionID != "" {
		if pane, err := h.terminal.FindSession(r.Context(), name, body.AgentType, body.SessionID); err == nil && pane == nil {
			writeTerminalError(w, ptymanager.ErrTerminalGone)
			return
		}
	}
	screen, ok := h.capturePromptScreen(r.Context(), name, body.AgentType, body.SessionID)
	if !ok || body.N > len(screen.Options) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "The prompt is no longer open. Check the terminal."})
		return
	}
	opt := screen.Options[body.N-1]
	if normalizeLabel(opt.Label) != normalizeLabel(body.Label) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "The prompt changed. Answer it in the terminal."})
		return
	}
	// A typed answer is one line: Enter submits the field
	text := strings.Join(strings.Fields(body.Text), " ")
	if opt.Action == "text" && text == "" {
		errBadRequest(w, "text is required for this option")
		return
	}
	action := opt.Action
	if screen.Unnumbered {
		action = "unnumbered" // its Enter is sent here, never by confirmAnswer
		if moved, err := h.selectUnnumbered(r.Context(), name, body.AgentType, body.SessionID, screen, body.N); err != nil {
			writeTerminalError(w, err)
			return
		} else if !moved {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "prompt_still_open",
				"error": "The prompt's selection didn't move to that option. Answer it in the terminal."})
			return
		}
	} else if err := h.terminal.SendRawInput(r.Context(), name, []string{strconv.Itoa(body.N)}, body.AgentType, body.SessionID); err != nil {
		writeTerminalError(w, err)
		return
	}
	if opt.Action == "text" && !screen.Unnumbered {
		// The digit turns the option into a text field; type into it, then Enter
		time.Sleep(promptTextDelay)
		if err := h.terminal.SendInput(r.Context(), name, text, body.AgentType, body.SessionID); err != nil {
			writeTerminalError(w, err)
			return
		}
	}
	h.hints.forget(body.SessionID) // the prompt was answered: the next poll reads what replaced it
	h.startupScreens.forget(promptReadKey(body.SessionID, name, body.AgentType))
	cleared, err := h.confirmAnswer(r.Context(), name, body.AgentType, body.SessionID, screen, body.N, action)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	if !cleared {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "prompt_still_open",
			"error": "The answer was sent, but the agent's prompt is still open. Check the terminal."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answered": opt.Label})
}

// selectUnnumbered answers option n of a menu without numbers: the arrow keys move its selection from the
// option selected now, and Enter is sent only once the screen shows the same menu with option n selected.
// It reports false, having sent no Enter, when that isn't seen in time (the menu changed or went).
func (h *SessionsHandler) selectUnnumbered(ctx context.Context, name, agentType, sessionID string, before promptScreen, n int) (bool, error) {
	as, isArrow := h.terminal.(ptymanager.ArrowSender)
	es, isEnter := h.terminal.(ptymanager.EnterSender)
	if !isArrow || !isEnter {
		return false, nil
	}
	from := 0
	for i, o := range before.Options {
		if o.Selected {
			from = i + 1
		}
	}
	steps, up := n-from, n < from
	if up {
		steps = -steps
	}
	for i := 0; i < steps; i++ {
		if err := as.SendArrow(ctx, name, agentType, sessionID, up); err != nil {
			return false, err
		}
	}
	deadline := time.Now().Add(promptClearTimeout)
	for {
		now, ok, err := h.readPromptScreen(ctx, name, agentType, sessionID)
		if err == nil && ok && samePrompt(before, now) && now.Options[n-1].Selected {
			return true, es.SendEnter(ctx, name, agentType, sessionID)
		}
		if err == nil && (!ok || !samePrompt(before, now)) || time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(promptPollEvery):
		}
	}
}

// confirmAnswer waits, a bounded time, for the prompt an answer was sent to to go, and says whether it went.
// Codex has menus a digit answers (its sign-in choice) and menus where a digit only moves the selection and
// Enter confirms it (its hook review, "Press enter to confirm"; #4543): for a Codex select option, while the
// same prompt stays up with that option selected for promptSettle, Enter is sent once. Claude Code's digits
// answer, so it never gets an Enter here. The screen is what is read: Codex runs no hook before its trust
// menus are answered, so there is no event to wait for.
func (h *SessionsHandler) confirmAnswer(ctx context.Context, name, agentType, sessionID string, before promptScreen, n int, action string) (bool, error) {
	mayConfirm := agentType == at.Codex && action == "select"
	deadline := time.Now().Add(promptClearTimeout)
	var heldSince time.Time
	for {
		now, ok, err := h.readPromptScreen(ctx, name, agentType, sessionID)
		if err == nil && (!ok || !samePrompt(before, now)) {
			return true, nil
		}
		if err == nil && mayConfirm && n <= len(now.Options) && now.Options[n-1].Selected {
			if heldSince.IsZero() {
				heldSince = time.Now()
			} else if time.Since(heldSince) >= promptSettle {
				es, isSender := h.terminal.(ptymanager.EnterSender)
				if !isSender {
					return false, nil
				}
				if err := es.SendEnter(ctx, name, agentType, sessionID); err != nil {
					return false, err
				}
				mayConfirm = false // one Enter, never a second into whatever comes next
				deadline = time.Now().Add(promptClearTimeout)
			}
		} else {
			heldSince = time.Time{}
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(promptPollEvery):
		}
	}
}
