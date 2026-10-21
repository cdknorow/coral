package board

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The merge queue's read and its landed record (#5689). Both go by the rule that the queue lands only approved bytes: a patch is a
// candidate when it is the newest row of its name on a completed task AND its digest is in the approving round's ApprovedDigests AND
// nobody has marked it landed. A row attached after the approval is never a candidate on the strength of it, and a superseded row stays
// on the task as history.

// QueueEntry is one approved, not yet landed task of the queue.
type QueueEntry struct {
	TaskID     int64          `json:"task_id"`
	Title      string         `json:"title"`
	Author     string         `json:"author"`
	Approvers  []string       `json:"approvers"`
	Round      int            `json:"round"`
	ApprovedAt string         `json:"approved_at"` // when the round was approved; the queue is oldest first by it
	Patches    []TaskArtifact `json:"patches"`     // the candidates only: newest of their name, approved, not landed. Rows from submit-review have no Seq
}

// LandedCommit is a commit of the batch that landed a task's patch: the queue and git, not the client-writable Landed flag, say what landed.
type LandedCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject,omitempty"`
}

const (
	landedCommitKind = "landed-commit"
	maxLandedCommits = 200
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// approvedRound is the round that approved the task and completed it: the last one, with Result approved.
func approvedRound(w *TaskWorkflow) *ReviewRound {
	n := len(w.ReviewRounds)
	if n == 0 || w.ReviewRounds[n-1].Result != "approved" {
		return nil
	}
	return &w.ReviewRounds[n-1]
}

func inSet(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// candidatePatches are the patch rows a queue would land on a completed task: the newest row of each patch name, whose digest the
// approving round approved, not yet marked landed.
func candidatePatches(w *TaskWorkflow) []TaskArtifact {
	round := approvedRound(w)
	if round == nil {
		return nil
	}
	var out []TaskArtifact
	for _, p := range currentPatches(w.Artifacts) {
		if inSet(round.ApprovedDigests, p.Digest) && p.LandedBy == "" {
			out = append(out, p)
		}
	}
	return out
}

// MergeQueue lists the approved, not yet landed tasks of a board, oldest approval first. It reads only what the board holds: how stale a
// base is and which patches conflict need git: the person who lands checks them (agent_docs/merge-queue.md).
func (s *Store) MergeQueue(ctx context.Context, project string) ([]QueueEntry, error) {
	rows, err := s.reader().QueryxContext(ctx, `SELECT t.id, t.title, t.completed_at, w.data FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		WHERE t.board_id = ? AND t.status = 'completed' AND w.data LIKE '%"approved_digests"%' ORDER BY t.id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueEntry{}
	for rows.Next() {
		var id int64
		var title string
		var completedAt *string
		var data string
		if err := rows.Scan(&id, &title, &completedAt, &data); err != nil {
			return nil, err
		}
		w, err := decodeWorkflow(data, true)
		if err != nil {
			return nil, err
		}
		cands := candidatePatches(&w)
		if len(cands) == 0 {
			continue
		}
		round := approvedRound(&w)
		e := QueueEntry{TaskID: id, Title: title, Author: w.OriginalAuthor, Round: round.Round, ApprovedAt: round.ConcludedAt, Patches: cands}
		if e.ApprovedAt == "" && completedAt != nil {
			e.ApprovedAt = *completedAt // a round approved before the time was recorded
		}
		for _, r := range round.Reviewers {
			if r.State == "approved" {
				e.Approvers = append(e.Approvers, r.Reviewer)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ApprovedAt != out[j].ApprovedAt {
			return out[i].ApprovedAt < out[j].ApprovedAt
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

// MarkLanded records that the patches with these digests of a completed task landed, and the commits that landed them (#5689). Only the
// board's orchestrator or the Operator (byOperator) may: a task's author marking his own patch landed would hide it from the queue. A
// digest the approving round did not approve, or that is not the newest row of its name, is refused with nothing recorded. It is
// idempotent: a row already marked keeps its first marker, and a commit already recorded is not added again.
func (s *Store) MarkLanded(ctx context.Context, project string, id int64, actor string, byOperator bool, digests []string, commits []LandedCommit) (*Task, error) {
	if !byOperator {
		if err := s.RequireTaskReviewer(ctx, project, actor); err != nil {
			return nil, err
		}
	}
	if len(digests) == 0 {
		return nil, fmt.Errorf("name the digests of the patches that landed")
	}
	if len(commits) > maxLandedCommits {
		return nil, fmt.Errorf("at most %d commits in one mark-landed", maxLandedCommits)
	}
	for _, c := range commits {
		if !shaRe.MatchString(c.SHA) {
			return nil, fmt.Errorf("commit %q is not a full 40-digit sha", c.SHA)
		}
		if !ValidLabel(c.Subject, 200) {
			return nil, fmt.Errorf("the subject of commit %s is not plain text of at most 200 bytes", c.SHA)
		}
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, fmt.Errorf("task #%d is not on this board", id)
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	round := approvedRound(&w)
	if status != "completed" || round == nil {
		return nil, fmt.Errorf("task #%d is not an approved, completed task: only those can be marked landed", id)
	}
	current := map[string]bool{}
	for _, p := range currentPatches(w.Artifacts) {
		current[p.Digest] = true
	}
	for _, d := range digests {
		if !inSet(round.ApprovedDigests, d) || !current[d] {
			return nil, fmt.Errorf("digest %s is not an approved patch of task #%d (a patch is landed only on the digests its approving round approved)", d, id)
		}
	}
	now := nowUTC()
	for i := range w.Artifacts {
		a := &w.Artifacts[i]
		if a.Kind == PatchKind && inSet(digests, a.Digest) && a.LandedBy == "" {
			a.LandedBy, a.LandedAt = actor, now
		}
	}
	have := map[string]bool{}
	for _, a := range w.Artifacts {
		if a.Kind == landedCommitKind {
			have[a.Revision] = true
		}
	}
	seq := maxSeq(w.Artifacts)
	for _, c := range commits {
		if have[c.SHA] {
			continue
		}
		have[c.SHA] = true
		seq++
		subject := strings.TrimSpace(c.Subject)
		if subject == "" {
			subject = "(no subject)"
		}
		w.Artifacts = append(w.Artifacts, TaskArtifact{Name: "landed-" + c.SHA[:12], Kind: landedCommitKind, Revision: c.SHA, Content: subject,
			Seq: seq, AttachedBy: actor, LandedBy: actor, LandedAt: now})
	}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getTaskByID(ctx, project, id)
}
