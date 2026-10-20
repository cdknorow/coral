package ptymanager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agyQuotaScreen is the screen of an Antigravity v1.4.2 CLI that had used up its quota (#5229): a peek of the live agent, rendered text without its styling. The
// Error ID is zeroed; nothing else was changed. The survey takes the input box's place.
const agyQuotaScreen = `● Ran 62 commands (+60 · coral-board read --id 1 2>&1 || true) (ctrl+o to expand)

⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in
103h13m54s.
Error ID: 00000000-0000-0000-0000-000000000000-0000

 How's the CLI experience so far? Help us improve:
 [1] Good  [2] Fine  [3] Bad  [0] Skip

? for shortcuts                                                           Gemini 3.8 Flash · high
`

func TestAgyQuotaOnTheLiveScreen(t *testing.T) {
	resets, ok := AgyQuotaOnScreen(agyQuotaScreen)
	require.True(t, ok, "the ⚠ line is the quota notice")
	assert.Equal(t, 103*time.Hour+13*time.Minute+54*time.Second, resets, "the reset time wrapped onto the next line")

	// The same notice on one line, and with the survey gone.
	one := strings.Replace(agyQuotaScreen, "Resets in\n103h13m54s.", "Resets in 2h5m.", 1)
	resets, ok = AgyQuotaOnScreen(one)
	assert.True(t, ok)
	assert.Equal(t, 2*time.Hour+5*time.Minute, resets)

	// A notice with no time still counts, with an unknown time.
	resets, ok = AgyQuotaOnScreen("⚠ Individual quota reached. Please upgrade.\n\n> \n")
	assert.True(t, ok)
	assert.Zero(t, resets)

	// Not the notice: the words inside a sentence, an agent quoting them far above, an ordinary screen.
	for name, s := range map[string]string{
		"in prose":   "The agent said the individual quota reached its limit earlier, so I retried.\n\n> \n",
		"scrolled":   "⚠ Individual quota reached. Resets in 1h.\n" + strings.Repeat("work\n", agyQuotaLines+2) + "> \n",
		"idle":       "● Done.\n\n> \n? for shortcuts      Gemini 3.8 Flash · high\n",
		"empty":      "",
		"bad number": "⚠ Quota exceeded. Resets in soon.\n",
	} {
		_, ok := AgyQuotaOnScreen(s)
		if name == "bad number" {
			assert.True(t, ok, name+": a notice, time unknown")
			continue
		}
		assert.False(t, ok, name)
	}
}

func TestAgySurveyIsOnlyThingOnScreen(t *testing.T) {
	assert.True(t, agySurveyOnlyOnScreen(agyQuotaScreen), "the live screen: the survey is the last block")

	// A draft in the input box: a digit would join it.
	draft := strings.Replace(agyQuotaScreen, "\n? for shortcuts", "\n> deploy the fix to staging\n? for shortcuts", 1)
	assert.False(t, agySurveyOnlyOnScreen(draft), "a person's draft")
	empty := strings.Replace(agyQuotaScreen, "\n? for shortcuts", "\n> \n? for shortcuts", 1)
	assert.True(t, agySurveyOnlyOnScreen(empty), "an empty box under the survey")

	for name, s := range map[string]string{
		"text under it":  strings.Replace(agyQuotaScreen, "\n? for shortcuts", "\nthe agent is writing\n? for shortcuts", 1),
		"no question":    strings.Replace(agyQuotaScreen, " How's the CLI experience so far? Help us improve:\n", "", 1),
		"other choices":  strings.Replace(agyQuotaScreen, "[1] Good  [2] Fine  [3] Bad  [0] Skip", "[1] Yes  [2] No  [0] Skip", 1),
		"a permission":   "Allow this command?\n [1] Yes  [2] No  [0] Skip\n\n? for shortcuts\n",
		"quoted, above":  agyQuotaScreen + strings.Repeat("later output\n", 6),
		"claude survey":  "● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n",
		"just the quota": "⚠ Individual quota reached.\nError ID: x\n\n? for shortcuts\n",
	} {
		assert.False(t, agySurveyOnlyOnScreen(s), name)
	}
}

// agyTerm shows agy's survey screen until a 0 is pressed, then an idle screen; keys and pastes are recorded.
type agyTerm struct {
	*surveyTerm
	box string // what the screen's input box holds, if it draws one
}

func (a *agyTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	a.dialogTerm.mu.Lock()
	defer a.dialogTerm.mu.Unlock()
	if a.dialogTerm.dialog {
		s := agyQuotaScreen
		if a.box != "" {
			s = strings.Replace(s, "\n? for shortcuts", "\n> "+a.box+"\n? for shortcuts", 1)
		}
		return s, nil
	}
	return "● Done.\n\n> \n", nil // the survey gone: a plain box (agy's own idle footer is not asserted here, no capture of it exists)
}

func agyInput(term *agyTerm, name, text string) AutomatedInput {
	in := dialogInput(term.dialogTerm, name, "k", text, false)
	in.Term = term
	in.AgentType = "agy"
	return in
}

// agy's survey is dismissed like Claude's (#5229): one 0, never 1, 2 or 3, and the held nudge goes in only after it is gone.
func TestAgySurveyIsDismissedWithZeroThenTheNudgeIsDelivered(t *testing.T) {
	surveyGate(t, time.Hour)
	term := &agyTerm{surveyTerm: newSurveyTerm(true, false)}
	name := uniqueName("agy-survey-dismiss")
	t.Cleanup(func() { DropHeld(name, "test") })
	text := "2 unread messages on the message board."
	_, _ = DeliverAutomated(context.Background(), agyInput(term, name, text))
	waitFor(t, "the nudge is typed after the survey is gone", func() bool {
		for _, e := range term.log() {
			if e == "paste:"+text {
				return true
			}
		}
		return false
	})
	assert.Equal(t, []string{"0"}, term.keys(), "one 0, never a rating")
	log := term.log()
	require.GreaterOrEqual(t, len(log), 2)
	assert.Equal(t, "key:0", log[0], "the 0 came before the nudge")
}

// A person's draft in the box: nothing is sent into it.
func TestAgySurveyIsLeftAloneOverADraft(t *testing.T) {
	surveyGate(t, time.Hour)
	term := &agyTerm{surveyTerm: newSurveyTerm(true, false), box: "deploy the fix"}
	name := uniqueName("agy-survey-draft")
	t.Cleanup(func() { DropHeld(name, "test") })
	_, _ = DeliverAutomated(context.Background(), agyInput(term, name, "a nudge"))
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, term.keys(), "no key into a draft")
}
