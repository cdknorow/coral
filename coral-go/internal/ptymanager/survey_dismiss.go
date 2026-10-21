package ptymanager

import (
	"context"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/terminalinput"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Claude Code sometimes draws a session survey above its input box ("● How is Claude doing this session?
// (optional)" over "1: Bad  2: Fine  3: Good  0: Dismiss"). It acts on single digit keys, so the gate holds
// every send behind it as a dialog (#4104), and nobody answers it: the agent sits with its messages held. When the
// survey is the only thing open, Coral dismisses it itself, with 0 and never 1, 2 or 3 (it rates nothing on a
// person's behalf), waits until it is gone from the screen, and delivers the held send as usual (#5114).

// surveyChoicesRe is the survey's choices line, exactly (the digits are Claude Code's own order and words).
var surveyChoicesRe = regexp.MustCompile(`^1: Bad\s+2: Fine\s+3: Good\s+0: Dismiss$`)

// surveyQuestionRe is the question line above it.
var surveyQuestionRe = regexp.MustCompile(`^●\s+How is Claude doing this session\?`)

// surveyOnlyOnScreen reports whether the only thing open on screen is Claude Code's session survey: the
// screen holds a dialog above the input box (PromptOnScreen, so quoted in scrollback or higher up it doesn't count) whose
// choices line is the survey's, exactly, under its question line. The bug-report box, a permission prompt or
// any other dialog is not it.
func surveyOnlyOnScreen(screen string) bool {
	open, why := PromptOnScreen(screen)
	if !open || !isDialogHold(why) {
		return false
	}
	choices, ok := dialogChoicesIn(why)
	if !ok || !surveyChoicesRe.MatchString(choices) {
		return false
	}
	return surveyQuestionAboveInputBox(tailLines(screenText(screen), promptScreenLines))
}

// draftInBox reports whether the input box holds text that may be a person's: anything but nothing, the agent's placeholder
// or Claude's faint suggestion for the next prompt (checked on the styled screen where the terminal has one, as a send's
// own box check is, #3468). Claude Code takes a digit as an answer to the survey only while nothing is being typed, so a 0
// sent over a draft would join the draft and dismiss nothing. A box Coral cannot read counts as a draft.
func (in AutomatedInput) draftInBox(ctx context.Context, screen string) bool {
	box, ok := inputBoxTextAny(screen)
	if !ok {
		return true
	}
	if boxPlaceholder(box) {
		return false
	}
	if !hasPlaceholder(screen) {
		if ss, ok := in.Term.(StyledScreenReader); ok {
			if styled, err := ss.AgentScreenStyled(ctx, in.Name, in.AgentType, in.SessionID); err == nil && suggestionOnly(styled) {
				return false
			}
		}
	}
	return true
}

// dialogChoicesIn is the choices line inside PromptOnScreen's dialog reason ("... above the input box (<choices>)").
func dialogChoicesIn(why string) (string, bool) {
	const lead = dialogWhy + "a dialog is open above the input box ("
	if !strings.HasPrefix(why, lead) || !strings.HasSuffix(why, ")") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(why, lead), ")"), true
}

// surveyQuestionAboveInputBox reports whether the line above the survey's choices line, which sits directly above
// the input box (dialogAboveInputBox's rule), is the survey's question.
func surveyQuestionAboveInputBox(lines []string) bool {
	in := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isInputBoxLine(lines[i]) {
			in = i
			break
		}
	}
	if in < 0 {
		return false
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
	return i > 0 && surveyChoicesRe.MatchString(strings.TrimSpace(lines[i])) && surveyQuestionRe.MatchString(strings.TrimSpace(lines[i-1]))
}

// RawInputSender is implemented by terminals that can type single keys into an agent's terminal (tmux key names, or
// the bytes on a PTY): what answers a digit-key dialog.
type RawInputSender interface {
	SendRawInput(ctx context.Context, o terminalinput.Origin, name string, keys []string, agentType, sessionID string) error
}

// surveyGoneWait is how long after the 0 the screen is read for the survey to be gone, surveyPoll how often, and
// surveyRetryGap how long before the 0 is sent again to a session whose survey did not go (a screen that does not
// change must not be sent digits every pass).
// (Fields of waits(), seams.go.)

// surveyOnScreen is the survey check for agentType's CLI: Claude Code's session survey, or Antigravity's feedback survey (#5229).
func surveyOnScreen(agentType, screen string) bool {
	if isAgyType(agentType) {
		return agySurveyOnlyOnScreen(screen)
	}
	return surveyOnlyOnScreen(screen)
}

func isAgyType(agentType string) bool { return agentType == at.Agy || agentType == at.Antigravity }

// dismissSurvey sends one 0 to name's terminal when the survey is the only thing open, waits for it to be gone from
// the screen, and reports whether it was: the held send is then looked at again, and typed only if nothing else holds
// it. It sends nothing for any other screen, over a draft in the input box (draftInBox), from a terminal that cannot type a key or show its screen, or again
// within surveyRetryGap of the last 0 (logged, so a survey that ignores it is seen); one line is logged per dismissal.
func (g *inputGate) dismissSurvey(ctx context.Context, in AutomatedInput) bool {
	keys, ok := in.Term.(RawInputSender)
	if !ok {
		return false
	}
	reader, ok := in.Term.(ScreenReader)
	if !ok {
		return false
	}
	screen, err := reader.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
	if err != nil || !surveyOnScreen(in.AgentType, screen) {
		return false
	}
	if !isAgyType(in.AgentType) && in.draftInBox(ctx, screen) { // agy's check (agySurveyOnlyOnScreen) already needs an empty box
		return false // a person is typing: a digit now joins their message, it does not answer the survey
	}
	g.mu.Lock()
	if g.surveyTried == nil {
		g.surveyTried = map[string]time.Time{}
	}
	if last, tried := g.surveyTried[in.Name]; tried && g.now().Sub(last) < waits().surveyRetryGap {
		g.mu.Unlock()
		return false
	}
	g.surveyTried[in.Name] = g.now()
	g.mu.Unlock()
	if err := keys.SendRawInput(ctx, terminalinput.Internal, in.Name, []string{"0"}, in.AgentType, in.SessionID); err != nil {
		slog.Warn("automated input: could not dismiss the agent's session survey", "session", in.Name, "error", err)
		return false
	}
	slog.Info("automated input: dismissed the agent's session survey with 0 (never a rating)", "session", in.Name)
	w := waits()
	deadline := time.Now().Add(w.surveyGoneWait)
	for {
		if screen, err := reader.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID); err == nil && !surveyOnScreen(in.AgentType, screen) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			slog.Warn("automated input: the agent's session survey is still on screen after the 0; the send stays held", "session", in.Name)
			return false
		}
		time.Sleep(w.surveyPoll)
	}
}
