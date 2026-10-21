package ptymanager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// surveyTerm is dialogTerm that also takes single keys: the 0 closes the survey unless it is told to ignore it, and every key
// and paste is recorded in order, so a test can say what came before what.
type surveyTerm struct {
	*dialogTerm
	logMu  sync.Mutex
	events []string
	ignore bool // the survey stays whatever is pressed
	quoted bool // the survey's words are in the scrollback, above other output, not above the input box
}

func newSurveyTerm(survey, ignore bool) *surveyTerm {
	return &surveyTerm{dialogTerm: &dialogTerm{dialog: true, survey: survey}, ignore: ignore}
}

func (s *surveyTerm) record(e string) {
	s.logMu.Lock()
	s.events = append(s.events, e)
	s.logMu.Unlock()
}
func (s *surveyTerm) log() []string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return append([]string(nil), s.events...)
}
func (s *surveyTerm) keys() []string {
	var out []string
	for _, e := range s.log() {
		if strings.HasPrefix(e, "key:") {
			out = append(out, strings.TrimPrefix(e, "key:"))
		}
	}
	return out
}

func (s *surveyTerm) SendRawInput(_ context.Context, _ terminalinput.Origin, _ string, keys []string, _, _ string) error {
	for _, k := range keys {
		s.record("key:" + k)
		if k == "0" && !s.ignore {
			s.dialogTerm.set(func(d *dialogTerm) { d.dialog = false })
		}
	}
	return nil
}

func (s *surveyTerm) SendPrompt(ctx context.Context, _ terminalinput.Origin, name, text, agentType, sessionID string) error {
	s.record("paste:" + text)
	return s.dialogTerm.SendPrompt(ctx, terminalinput.Unclassified, name, text, agentType, sessionID)
}

func surveyGate(t *testing.T, retryGap time.Duration) {
	t.Helper()
	dialogGate(t, time.Hour)
	setWaits(t, func(w *waitTimings) {
		w.surveyGoneWait, w.surveyPoll, w.surveyRetryGap = 300*time.Millisecond, 10*time.Millisecond, retryGap
	})
}

// Claude's session survey is the only thing open and a nudge is held behind it: Coral sends one 0 (never 1, 2 or 3),
// waits for the survey to be gone, and only then delivers the nudge, whose text starts with a digit that would
// have answered it.
func TestSurveyIsDismissedWithZeroThenTheNudgeIsDelivered(t *testing.T) {
	surveyGate(t, time.Hour)
	term := newSurveyTerm(true, false)
	name := uniqueName("claude-survey-dismiss")
	t.Cleanup(func() { DropHeld(name, "test") })
	text := "2 unread messages on the message board."
	in := dialogInput(term.dialogTerm, name, "k", text, false)
	in.Term = term
	_, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	waitFor(t, "the nudge to be delivered", func() bool { _, submitted, _, _, _ := term.state(); return len(submitted) == 1 })
	assert.Equal(t, []string{"0"}, term.keys(), "exactly one 0, and no rating")
	assert.Equal(t, []string{"key:0", "paste:" + text}, term.log(), "the 0 first, the nudge only after the survey was gone")
	_, submitted, _, whileDialog, _ := term.state()
	assert.Equal(t, []string{text}, submitted)
	assert.Zero(t, whileDialog, "nothing was typed while the survey was up")
}

// The survey's words quoted in the scrollback (not directly above the input box) are no survey: nothing is
// pressed, and the nudge goes as usual.
func TestSurveyQuotedInScrollbackGetsNoZero(t *testing.T) {
	surveyGate(t, time.Hour)
	term := newSurveyTerm(false, false)
	term.dialogTerm.set(func(d *dialogTerm) { d.dialog = false })
	term.quoted = true
	name := uniqueName("claude-survey-quoted")
	t.Cleanup(func() { DropHeld(name, "test") })
	in := dialogInput(term.dialogTerm, name, "k", "hello", false)
	in.Term = term
	_, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	waitFor(t, "delivery", func() bool { _, submitted, _, _, _ := term.state(); return len(submitted) == 1 })
	assert.Empty(t, term.keys(), "no 0 for a survey that is only quoted")
}

func (s *surveyTerm) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	if s.quoted {
		_, _, _, _, box := s.dialogTerm.state()
		return claudeScreen(neutralScrollback+surveyLines+"⏺ Anyway, I quoted the survey above; carrying on.\n\n", box, false), nil
	}
	return s.dialogTerm.AgentScreen(ctx, name, agentType, sessionID)
}

// The bug-report dialog is not the survey: it keeps holding, and no key is pressed for it.
func TestBugReportDialogIsNotDismissed(t *testing.T) {
	surveyGate(t, time.Hour)
	term := newSurveyTerm(false, false) // the bug-report box
	name := uniqueName("claude-survey-bugreport")
	t.Cleanup(func() { DropHeld(name, "test") })
	in := dialogInput(term.dialogTerm, name, "k", "2 to send? no: a board nudge", false)
	in.Term = term
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, term.keys(), "no key for the bug-report box")
	pasted, _, enters, whileDialog, _ := term.state()
	assert.Empty(t, pasted)
	assert.Zero(t, enters+whileDialog)
}

// A survey that does not go with the 0 is sent the 0 once, not every pass, and the held send (which starts with
// a digit that would answer it) is never typed into it.
func TestSurveyThatIgnoresTheZeroIsNotPressedAgainAndHoldsTheSend(t *testing.T) {
	surveyGate(t, time.Hour)
	term := newSurveyTerm(true, true)
	name := uniqueName("claude-survey-ignores")
	t.Cleanup(func() { DropHeld(name, "test") })
	in := dialogInput(term.dialogTerm, name, "k", "3 new tasks", false)
	in.Term = term
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(400 * time.Millisecond) // many passes (every 20 ms) and the wait for the survey to go
	assert.Equal(t, []string{"0"}, term.keys(), "the 0 once")
	pasted, submitted, enters, whileDialog, _ := term.state()
	assert.Empty(t, pasted)
	assert.Empty(t, submitted)
	assert.Zero(t, enters+whileDialog, "the held send was never typed into the survey")
	assert.Equal(t, 1, HeldCount(name))
}

// The screens that are the survey, and those that are not.
func TestSurveyOnlyOnScreen(t *testing.T) {
	assert.True(t, surveyOnlyOnScreen(claudeScreen(neutralScrollback+surveyLines, "", false)))
	assert.True(t, surveyOnlyOnScreen(claudeScreen(neutralScrollback+surveyLines, "half a draft", false)))
	assert.False(t, surveyOnlyOnScreen(claudeScreen(neutralScrollback, "", true)), "the bug-report box")
	assert.False(t, surveyOnlyOnScreen(claudeScreen(neutralScrollback, "", false)), "no dialog")
	for name, lines := range map[string]string{
		"another question":     "● Would you like to continue? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n",
		"another choice":       "● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   4: Great   0: Dismiss\n\n",
		"no dismiss":           "● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good\n\n",
		"agent bullet":         "⏺ How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n",
		"choices only":         "  1: Bad    2: Fine   3: Good   0: Dismiss\n\n",
		"question further up":  "● How is Claude doing this session? (optional)\n⏺ other output\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n",
		"trailing text":        "● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss (esc)\n\n",
		"different order":      "● How is Claude doing this session? (optional)\n  0: Dismiss   1: Bad    2: Fine   3: Good\n\n",
		"quoted, output below": surveyLines + "⏺ I quoted it.\n\n",
	} {
		assert.False(t, surveyOnlyOnScreen(claudeScreen(neutralScrollback+lines, "", false)), name)
	}
}
