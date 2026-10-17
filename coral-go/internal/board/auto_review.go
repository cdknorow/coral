package board

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// Review auto-delegation (#4175, the first self-acting rule): a task that enters review with
// no reviewers goes to the reviewers it names, instead of waiting for the orchestrator to
// delegate it. Security's #4176 rules 12 and 13 shape who that can be.

// Where AutoReviewers found its reviewers, for the board line.
const (
	ReviewersNamedInTask = "named in the task"
	ReviewersTeamDefault = "the team's default reviewers"
)

// reviewersLine is a "Reviewers: A, B" line in a task's body.
var reviewersLine = regexp.MustCompile(`(?im)^[ \t]*reviewers?[ \t]*:[ \t]*(.+)$`)

// splitReviewerNames splits "Security Reviewer, QA Engineer and UX" into names.
func splitReviewerNames(s string) []string {
	s = strings.TrimRight(strings.TrimSpace(s), ".")
	var out []string
	for _, part := range regexp.MustCompile(`\s*,\s*|\s+and\s+`).Split(s, -1) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// AutoReviewers returns who Coral delegates task's review to as it enters review, and where
// the names came from; none (the orchestrator delegates, as before) when nobody qualifies.
//
//   - The names a task carries (its reviewers field, else a "Reviewers:" line in its body) count
//     only when everyone who made or amended it is the orchestrator or the Operator: an agent
//     can't name its own reviewers (#4176 rule 13). Otherwise, the team's default reviewers
//     (its health settings; none by default).
//   - A reviewer is a current subscriber of the board, never the task's author, creator or
//     assignee (any spelling of the same name), and named once (#4176 rule 12).
//
// It is called once, when the task enters review: the list is frozen then, as the review
// round DelegateReview records.
//
// Who made or amended a task is trusted one of two ways (#4255, identity phase 2). By name (the
// default, "log"): the creator's or amender's name is the Operator's or the orchestrator's. By how the
// name was backed when it was recorded (byIdentity, "enforce"): the Operator verified (recorded
// "operator"), or the orchestrator by an agent token (recorded "token"); a name only claimed, or one
// recorded by no one (a task from before it was recorded, an amendment whose record failed), is an
// agent's and is not trusted. identityDiffers reports, for a task that names reviewers, that the two
// disagree, so the switch can be judged on numbers before it is turned on.
func (s *Store) AutoReviewers(ctx context.Context, project string, task *Task, orch string, byIdentity bool) (names []string, source string, identityDiffers bool, err error) {
	trustedName := func(actor string) bool {
		return actor == "Operator" || (orch != "" && SameIdentity(actor, orch))
	}
	trustedIdentity := func(actor, identity string) bool {
		switch identity {
		case "operator":
			return true
		case "token":
			return orch != "" && SameIdentity(actor, orch)
		}
		return false
	}
	okName := trustedName(task.CreatedBy)
	okIdentity := trustedIdentity(task.CreatedBy, task.Workflow.CreatedIdentity)
	for _, a := range task.Workflow.Amendments {
		if !trustedName(a.Actor) {
			okName = false
		}
		if !trustedIdentity(a.Actor, task.Workflow.AmendIdentity[strconv.Itoa(a.Revision)]) {
			okIdentity = false
		}
	}
	ok := okName
	if byIdentity {
		ok = okIdentity
	}
	var candidates []string
	named := func() []string {
		c := task.Workflow.Reviewers
		if len(c) == 0 && task.Body != nil {
			if m := reviewersLine.FindStringSubmatch(*task.Body); m != nil {
				c = splitReviewerNames(m[1])
			}
		}
		return c
	}
	identityDiffers = okName != okIdentity && len(named()) > 0
	if ok {
		candidates = named()
		source = ReviewersNamedInTask
	}
	if len(candidates) == 0 {
		candidates = s.GetHealthSettings(ctx, project).DefaultReviewers
		source = ReviewersTeamDefault
	}
	excluded := []string{task.CreatedBy, task.Workflow.OriginalAuthor}
	if task.AssignedTo != nil {
		excluded = append(excluded, *task.AssignedTo)
	}
	if task.Workflow.CompletionReview != nil {
		excluded = append(excluded, task.Workflow.CompletionReview.SubmittedBy)
	}
	subs, err := s.ListSubscribers(ctx, project) // the board's current members
	if err != nil {
		return nil, "", identityDiffers, err
	}
	for _, c := range candidates {
		skip := false
		for _, e := range append(excluded, names...) {
			if e != "" && SameIdentity(c, e) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, sub := range subs {
			if SameIdentity(c, sub.SubscriberID) {
				names = append(names, sub.SubscriberID)
				break
			}
		}
	}
	if len(names) == 0 {
		return nil, "", identityDiffers, nil
	}
	return names, source, identityDiffers, nil
}

// SetBoardSetting stores one board setting's raw value.
func (s *Store) SetBoardSetting(ctx context.Context, project, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO board_settings(project, key, value) VALUES (?, ?, ?)
		ON CONFLICT(project, key) DO UPDATE SET value = excluded.value`, project, key, value)
	return err
}

// BoardSetting is one board setting's raw value, "" when unset.
func (s *Store) BoardSetting(ctx context.Context, project, key string) (string, error) {
	return s.boardSettingValue(ctx, project, key)
}

// boardSettingValue is one board setting's raw value, "" when unset.
func (s *Store) boardSettingValue(ctx context.Context, project, key string) (string, error) {
	var v []string
	if err := s.db.SelectContext(ctx, &v, `SELECT value FROM board_settings WHERE project = ? AND key = ?`, project, key); err != nil {
		return "", err
	}
	if len(v) == 0 {
		return "", nil
	}
	return v[0], nil
}
