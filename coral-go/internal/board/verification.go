package board

import (
	"strings"
)

// A completed task's verification state (#4563): whether "done" can be trusted at a glance. The server computes it from the
// review and gate records, never from anything the agent sets; it is a label only and blocks nothing.
//
//	verified    a reviewer other than the task's author approved it through the review flow (its final round was approved), or its
//	            registered completion gates all passed for the accepted candidate revision
//	evidence    no independent check, but the completion carries artifacts
//	unverified  neither
//
// Only a completed task with outcome success has a state; a failed, cancelled or unfinished task has none, and neither has a review
// task (the verdict record of someone else's task, not a work product).

// Verification states.
const (
	VerificationVerified   = "verified"
	VerificationEvidence   = "evidence"
	VerificationUnverified = "unverified"
)

// Verification is the state and what backs it.
type Verification struct {
	State string `json:"state"`
	// By is the approving reviewers' names and "gate:<type>" for each gate that passed; empty for evidence and unverified.
	By []string `json:"by"`
	// At is when the newest approval or gate pass happened, or when the task was completed for evidence and unverified.
	At string `json:"at,omitempty"`
}

// verificationOf computes t's state from its workflow, which must be attached (finishWorkflow). nil when t has none.
func verificationOf(t *Task) *Verification {
	w := t.Workflow
	if t.Status != "completed" || w.Outcome != "success" || w.ReviewOf != 0 {
		return nil
	}
	v := &Verification{By: []string{}}
	var latest string
	note := func(by, at string) {
		v.By = append(v.By, by)
		if at > latest {
			latest = at
		}
	}
	// Reviewers: the approved round (the review flow ends the task when every reviewer of a round approved), never the author.
	for _, round := range w.ReviewRounds {
		if round.Result != "approved" {
			continue
		}
		for _, r := range round.Reviewers {
			if r.State == "approved" && !strings.EqualFold(strings.TrimSpace(r.Reviewer), strings.TrimSpace(w.OriginalAuthor)) && !containsFold(v.By, r.Reviewer) {
				note(r.Reviewer, r.At)
			}
		}
	}
	// Gates: the task has registered completion gates, each has a result, every result passed, and all were checked against the
	// revision that was accepted.
	if gatesPassed(w) {
		for _, g := range w.GateResults {
			note("gate:"+g.Type, g.CheckedAt)
		}
	}
	switch {
	case len(v.By) > 0:
		v.State, v.At = VerificationVerified, latest
	case len(w.Artifacts) > 0:
		v.State, v.At = VerificationEvidence, ptrStr(t.CompletedAt)
	default:
		v.State, v.At = VerificationUnverified, ptrStr(t.CompletedAt)
	}
	return v
}

func gatesPassed(w TaskWorkflow) bool {
	if len(w.CompletionGates) == 0 || len(w.GateResults) < len(w.CompletionGates) {
		return false
	}
	for _, g := range w.GateResults {
		if !g.Passed || g.CandidateRevision != w.CandidateRevision {
			return false
		}
	}
	return true
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// verificationSQL is the filter for each state, the same rules as verificationOf over the stored workflow JSON (the query joins
// task_workflows as w). A test checks the two agree on every kind of task.
const (
	sqlCompletedSuccess = `(board_tasks.status = 'completed' AND COALESCE(json_extract(w.data, '$.outcome'), '') IN ('', 'success') AND COALESCE(json_extract(w.data, '$.review_of'), 0) = 0)`
	sqlReviewerApproved = `EXISTS (SELECT 1 FROM json_each(w.data, '$.review_rounds') r, json_each(r.value, '$.reviewers') v
		WHERE json_extract(r.value, '$.result') = 'approved' AND json_extract(v.value, '$.state') = 'approved'
		AND lower(trim(COALESCE(json_extract(v.value, '$.reviewer'), ''))) != lower(trim(COALESCE(json_extract(w.data, '$.original_author'), ''))))`
	sqlGatesPassed = `(json_array_length(COALESCE(json_extract(w.data, '$.completion_gates'), '[]')) > 0
		AND json_array_length(COALESCE(json_extract(w.data, '$.gate_results'), '[]')) >= json_array_length(COALESCE(json_extract(w.data, '$.completion_gates'), '[]'))
		AND NOT EXISTS (SELECT 1 FROM json_each(w.data, '$.gate_results') g
			WHERE json_extract(g.value, '$.passed') IS NOT 1
			OR COALESCE(json_extract(g.value, '$.candidate_revision'), '') != COALESCE(json_extract(w.data, '$.candidate_revision'), '')))`
	sqlVerified = `(` + sqlReviewerApproved + ` OR ` + sqlGatesPassed + `)`
	sqlEvidence = `json_array_length(COALESCE(json_extract(w.data, '$.artifacts'), '[]')) > 0`
)

var verificationSQL = map[string]string{
	VerificationVerified:   `(` + sqlCompletedSuccess + ` AND ` + sqlVerified + `)`,
	VerificationEvidence:   `(` + sqlCompletedSuccess + ` AND NOT ` + sqlVerified + ` AND ` + sqlEvidence + `)`,
	VerificationUnverified: `(` + sqlCompletedSuccess + ` AND NOT ` + sqlVerified + ` AND NOT (` + sqlEvidence + `))`,
}
