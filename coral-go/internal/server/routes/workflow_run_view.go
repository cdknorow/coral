package routes

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/subgentic/coral/internal/clitext"
	"github.com/subgentic/coral/internal/store"
)

// What a run shows without opening its logs (#6530, #6515's third finding): how long it took, and for a failed run which step failed and why in one line. All of it is derived from what
// the run already stores (its times, its step results and its error message); each step in a run's detail also carries "number", counted from 1.
//
// The numbers: a step's "index" and the run's "current_step" are counted from 0 and are what the step-file route (/steps/{stepIndex}/files) takes, so they are not changed; "number"
// and failed_step.number are counted from 1, as the workflow form and its warnings say "step 1".

const maxFailureReason = 200 // characters

// runView fills in the computed fields of a run. It never changes the stored fields.
func runView(run *store.WorkflowRun) {
	if run == nil {
		return
	}
	run.DurationSeconds = runDuration(run)
	for i, raw := range run.Steps { // the detail's steps carry their number from 1 beside the 0-based index
		var step map[string]json.RawMessage
		if json.Unmarshal(raw, &step) != nil {
			continue
		}
		var idx int
		if v, ok := step["index"]; ok && json.Unmarshal(v, &idx) == nil {
			step["number"], _ = json.Marshal(idx + 1)
		} else {
			step["number"], _ = json.Marshal(i + 1)
		}
		if b, err := json.Marshal(step); err == nil {
			run.Steps[i] = b
		}
	}
	if run.Status != "failed" {
		return
	}
	run.FailedStep, run.FailureReason = failureOf(run)
}

// runDuration is the seconds from the run's start to its end, nil until both are known (a run that has not finished has no duration yet).
func runDuration(run *store.WorkflowRun) *float64 {
	if run.StartedAt == nil || run.FinishedAt == nil {
		return nil
	}
	start, err1 := time.Parse(time.RFC3339Nano, *run.StartedAt)
	end, err2 := time.Parse(time.RFC3339Nano, *run.FinishedAt)
	if err1 != nil || err2 != nil || end.Before(start) {
		return nil
	}
	d := end.Sub(start).Seconds()
	return &d
}

// failureOf finds the first failed step in the run's results and a one-line reason: the end of that step's stderr, else of its output, else the run's own error message.
func failureOf(run *store.WorkflowRun) (*store.FailedStep, string) {
	var results []struct {
		Index      int    `json:"index"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		ErrorTail  string `json:"error_tail"`
		OutputTail string `json:"output_tail"`
		Note       string `json:"note"`
	}
	if run.StepResults != "" {
		_ = json.Unmarshal([]byte(run.StepResults), &results)
	}
	var step *store.FailedStep
	reason := ""
	for i, r := range results {
		if r.Status != "failed" {
			continue
		}
		n := r.Index + 1
		if r.Index < 0 || r.Index > 10000 {
			n = i + 1
		}
		step = &store.FailedStep{Number: n, Name: runOneLine(r.Name, 80)}
		reason = reasonOf(r.ErrorTail, true)
		if reason == "" {
			reason = reasonOf(r.OutputTail, true)
		}
		if reason == "" {
			reason = reasonOf(r.Note, false)
		}
		break
	}
	if reason == "" && run.ErrorMsg != nil {
		reason = reasonOf(*run.ErrorMsg, false)
	}
	return step, reason
}

// reasonOf is the one line a tail of text comes to: the WHOLE text is cleaned and redacted first (a failed command may have printed a secret, and a secret is found by what is around it: a
// private key by its header and footer, a token by its whole length), then its last line with anything on it is taken, then that is cut. Cut or split first and a token's start, or a line
// from inside a key, would be shown alone (Security's #6530 finding). Empty when the text cannot be checked for secrets.
// A tail of output is reduced to its last line; a sentence (the step's note, the run's error message) is kept whole, as one line.
func reasonOf(text string, lastLineOnly bool) string {
	text = boundedText(text, lastLineOnly)
	if reasonInputSeen != nil {
		reasonInputSeen(len(text))
	}
	red, ok := redactComment(clitext.Clean(text))
	if !ok {
		return ""
	}
	if lastLineOnly {
		return lastLine(red)
	}
	if l := runOneLine(red, maxFailureReason); !isBlob(l) {
		return l
	}
	return ""
}

// blobRun is one unbroken run of 12 or more characters of base64, hex or a token: no space, no dot, no colon (Security suggested 32; a tail that ends in the middle of a key line leaves a fragment
// shorter than a line, so the floor is lower: a fragment of 11 characters or fewer is under 70 bits). Such a line is not a reason, and a redactor that was not given the header of a
// private key (a window or a stored tail that begins or ends inside one) cannot tell its lines from text, so the reason never shows one (Security's #6530 finding). Ordinary reasons have a
// space or punctuation in them, and a path or URL has a dot or a colon.
var blobRun = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{12,}$`)

func isBlob(line string) bool { return blobRun.MatchString(line) }

// lastLine is the last line of text that has anything on it, as one clean line.
func lastLine(text string) string {
	lines := strings.Split(clitext.Clean(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := runOneLine(lines[i], maxFailureReason)
		if l != "" && !isBlob(l) { // a blob is skipped for the line before it, or for no reason at all
			return l
		}
	}
	return ""
}

// runOneLine is s without control characters, runs of white space as one space, cut to max characters.
func runOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(clitext.Clean(s)), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max])) + "…"
	}
	return s
}

// maxReasonInput bounds the text a reason is made from, so the cost of a list of runs does not depend on how much a step wrote (the tails are bounded where they are stored; this keeps it so).
const maxReasonInput = 8 << 10

// boundedText is at most maxReasonInput bytes of text: the END of a tail (the last line is wanted), the START of a sentence, never cut inside a character.
func boundedText(text string, tail bool) string {
	if len(text) <= maxReasonInput {
		return text
	}
	if tail {
		text = text[len(text)-maxReasonInput:]
		if i := strings.IndexByte(text, '\n'); i >= 0 { // the window's first line is partial: dropped, so nothing begins in the middle of a token
			text = text[i+1:]
		}
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
		return text
	}
	text = text[:maxReasonInput]
	for len(text) > 0 && !utf8.ValidString(text[len(text)-1:]) && !utf8.RuneStart(text[len(text)-1]) {
		text = text[:len(text)-1]
	}
	return strings.ToValidUTF8(text, "")
}

// reasonInputSeen is a test seam: it is told how many bytes a reason sends to the redactor.
var reasonInputSeen func(n int)
