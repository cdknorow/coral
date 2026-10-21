package ptymanager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/terminalinput"
)

// draftSurveyTerm shows a fixed screen and records the raw keys typed into it.
type draftSurveyTerm struct {
	mu     sync.Mutex
	screen string
	keys   []string
}

func (d *draftSurveyTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	return d.screen, nil
}

func (d *draftSurveyTerm) SendRawInput(_ context.Context, _ terminalinput.Origin, _ string, keys []string, _, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keys = append(d.keys, keys...)
	return nil
}

// #5114 review: a person's half-typed message is in the input box while the survey is up. Claude Code takes a digit as an answer to the survey only
// while it knows no text is being typed (a digit typed into a message is part of the message), so a 0 sent now may not dismiss the survey but join the
// person's draft. Coral should leave a survey with a draft in the box alone (the person is there, and the survey goes when they send).
// Mutation: none; this test shows what the patch does today with a draft: it sends the 0.
func TestSurveyWithADraftInTheInputBoxGetsNoZero(t *testing.T) {
	surveyGate(t, time.Hour)
	term := &draftSurveyTerm{screen: claudeScreen(neutralScrollback+surveyLines, "half a draft of a message", false)}
	in := AutomatedInput{Term: term, Name: uniqueName("claude-survey-draft"), AgentType: "claude", SessionID: "sid"}
	assert.False(t, gate.dismissSurvey(context.Background(), in), "the survey is not dismissed over a person's draft")
	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.keys, "no key typed into an input box that holds a draft")
}

// styledSurveyTerm also shows the styled screen (capture-pane -e), as tmux does.
type styledSurveyTerm struct {
	draftSurveyTerm
	styled string
}

func (s *styledSurveyTerm) AgentScreenStyled(context.Context, string, string, string) (string, error) {
	return s.styled, nil
}

// styledSurveyScreen is the survey over an input box holding box (written with its own SGR sequences).
func styledSurveyScreen(box string) string {
	rule := strings.Repeat("─", 40)
	return "⏺ Done.\n\n● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n" + rule + "\n❯ " + box + "\n" + rule + "\n  ⏵⏵ auto mode on\n"
}

// Claude's faint suggestion for the next prompt fills the empty box after a turn (#3468): it is not a draft, so the survey is dismissed (else the
// survey that comes at the end of a turn would stay for ever); the same words typed at normal intensity, or a paste placeholder, are a draft.
func TestSurveyOverASuggestionIsDismissedButNotOverADraftOrAPaste(t *testing.T) {
	for name, c := range map[string]struct {
		box      string
		wantKeys []string
	}{
		"faint suggestion":    {"\x1b[2mcoral-board read\x1b[0m", []string{"0"}},
		"empty box":           {"", []string{"0"}},
		"normal intensity":    {"coral-board read", nil},
		"draft then ghost":    {"git reset \x1b[2mmain\x1b[0m", nil},
		"a paste placeholder": {"\x1b[2m[Pasted text #1 +12 lines]\x1b[0m", nil},
	} {
		t.Run(name, func(t *testing.T) {
			surveyGate(t, time.Hour)
			styled := styledSurveyScreen(c.box)
			term := &styledSurveyTerm{styled: styled}
			term.screen = stripSGR(styled)
			in := AutomatedInput{Term: term, Name: uniqueName("claude-survey-ghost-" + strings.ReplaceAll(name, " ", "-")), AgentType: "claude", SessionID: "sid"}
			gate.dismissSurvey(context.Background(), in)
			term.mu.Lock()
			defer term.mu.Unlock()
			assert.Equal(t, c.wantKeys, term.keys)
		})
	}
}

// stripSGR is the plain screen of a styled one.
func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }
