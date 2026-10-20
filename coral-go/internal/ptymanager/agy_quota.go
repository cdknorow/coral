package ptymanager

import (
	"regexp"
	"strings"
	"time"
)

// Antigravity's quota notice and feedback survey (#5229), from the v1.4.2 CLI's screen (a peek of an agy that had used up its quota):
//
//	⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in
//	103h13m54s.
//	Error ID: <uuid>
//
//	 How's the CLI experience so far? Help us improve:
//	 [1] Good  [2] Fine  [3] Bad  [0] Skip
//
//	? for shortcuts                                       Gemini 3.8 Flash · high
//
// The agent can do nothing until the quota resets, and the survey (answered by one digit key) sits under the notice with nobody to answer it.

// agyQuotaRe is the notice's first line: the message at the start of a line, behind the symbols the CLI draws before it (⚠, ■, │ ...).
var agyQuotaRe = regexp.MustCompile(`(?i)^[^\p{L}\p{N}]*(individual quota reached\b|allocation quota reached\b|you have exhausted your quota\b|you've exceeded your current quota\b|quota exceeded\b)`)

// agyResetsRe is the reset time, which the CLI wraps onto the next line at a narrow width: "Resets in 103h13m54s".
var agyResetsRe = regexp.MustCompile(`(?i)\bresets? in\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)`)

// agyQuotaLines is how much of the screen's bottom counts.
const agyQuotaLines = 16

// AgyQuotaOnScreen reports whether agy's screen shows its quota-reached notice near the bottom, and how long until the quota resets (0 when the
// screen does not say, or says something that is not a time).
func AgyQuotaOnScreen(screen string) (resets time.Duration, ok bool) {
	lines := tailLines(screenText(screen), agyQuotaLines)
	for i := len(lines) - 1; i >= 0; i-- {
		if !agyQuotaRe.MatchString(lines[i]) {
			continue
		}
		text := lines[i]
		if i+1 < len(lines) {
			text += " " + strings.TrimSpace(lines[i+1]) // the reset time wraps
		}
		if m := agyResetsRe.FindStringSubmatch(text); m != nil && m[1] != "" {
			if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
				resets = d
			}
		}
		return resets, true
	}
	return 0, false
}

// agySurveyChoicesRe is the survey's choices line, exactly; agySurveyQuestionRe the question above it.
var (
	agySurveyChoicesRe  = regexp.MustCompile(`^\[1\] Good\s+\[2\] Fine\s+\[3\] Bad\s+\[0\] Skip$`)
	agySurveyQuestionRe = regexp.MustCompile(`^How's the CLI experience so far\?`)
	agyStatusLineRe     = regexp.MustCompile(`^\? for shortcuts\b`)
)

// agySurveyChoices is the survey's choices line as the hold's reason names it.
const agySurveyChoices = "[1] Good  [2] Fine  [3] Bad  [0] Skip"

// agySurveyOnlyOnScreen reports whether the survey is the last thing on agy's screen: its choices line, exactly, under its question line, with
// nothing under it but blank lines and the status line, and no input box with anything in it (a person's draft: a digit typed into it
// joins the draft). The survey takes the place of the input box, so a screen without one is the expected case; a box that is there must be
// empty. Any other dialog, or text under the survey, is not it.
func agySurveyOnlyOnScreen(screen string) bool {
	lines := tailLines(screenText(screen), promptScreenLines)
	choices := -1
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "", agyStatusLineRe.MatchString(t):
			continue
		case isInputBoxLine(lines[i]):
			if !boxPlaceholder(inputBoxRe.ReplaceAllString(lines[i], "")) {
				return false // a draft: a digit typed into it joins it
			}
			continue
		case agySurveyChoicesRe.MatchString(t):
			choices = i
		}
		break
	}
	if choices < 1 {
		return false
	}
	q := choices - 1
	for q >= 0 && strings.TrimSpace(lines[q]) == "" {
		q--
	}
	if q < 0 || !agySurveyQuestionRe.MatchString(strings.TrimSpace(lines[q])) {
		return false
	}
	return true
}
