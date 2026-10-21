package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptr"
)

// reviewVerdict was split for #4599 (second ranking, item 7). It decides approvals and rejections and when a task
// is complete, so the old function is kept below (verbatim but for its name). Before every verdict board A's
// database is copied to B; the old verdict runs on A and the new on B; the result (or the refusal) and every row of
// both databases afterwards must be equal. A floor per outcome fails the test if one went unexercised.

var verdictOutcomes = map[string]int{
	"approved, round still open": 40, "approved, round complete": 15, "rejected": 40,
	"is not on this board": 10, "is back with": 20, "has no open review": 40, "you reviewed round": 20,
	"only the reviewers": 40, "you already": 30, "is not open": 40, "the note is too long": 10,
}

var (
	verdictSeen   map[string]int
	verdictSeenMu sync.Mutex
)

// seenVerdict counts one compared outcome; the seeds run in parallel.
func seenVerdict(outcome string) {
	verdictSeenMu.Lock()
	verdictSeen[outcome]++
	verdictSeenMu.Unlock()
}

// Each seed has its own directory, databases and random source, so the 60 seeds run in parallel (#4972: they were
// 273 s of the package's 472 s on epoch, run one after another). They run inside one group so the floors below are
// read only once every seed has finished.
func TestReviewVerdictAgreesWithBefore4599(t *testing.T) {
	verdictSeen = map[string]int{}
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= 60; seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				runVerdictOracle(t, seed)
			})
		}
	})
	t.Logf("compared, per outcome: %v", verdictSeen)
	for outcome, floor := range verdictOutcomes {
		require.GreaterOrEqual(t, verdictSeen[outcome], floor, "%q went (almost) unexercised", outcome)
	}
}

func copyVerdictBoard(t *testing.T, from, to string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(to + suffix)
	}
	src, err := os.Open(from)
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.Create(to)
	require.NoError(t, err)
	_, err = io.Copy(dst, src)
	require.NoError(t, err)
	require.NoError(t, dst.Close())
}

// stampRe: the timestamps the store writes with its own clock (the verdict's time), which differ between the two
// runs by the moments between them.
var stampRe = regexp.MustCompile(`\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(\.\d+)?Z?`)

func dumpVerdictBoard(t *testing.T, path string) []string {
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		tables = append(tables, n)
	}
	rows.Close()
	var out []string
	for _, tbl := range tables {
		r, err := db.Query(fmt.Sprintf("SELECT * FROM %q", tbl))
		require.NoError(t, err)
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, r.Scan(ptrs...))
			var b strings.Builder
			b.WriteString(tbl)
			for i, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, " %s=%v", cols[i], v)
			}
			out = append(out, maskStamps(b.String()))
		}
		r.Close()
	}
	sort.Strings(out)
	return out
}

// verdictResult is a verdict as compared: its fields (the tasks as JSON, their stamps masked) or its refusal.
func verdictResult(v *ReviewVerdict, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	b, _ := json.Marshal(v)
	return maskStamps(string(b))
}

// concludedRe: a round approved after the old verdict was frozen records when it concluded (ConcludedAt, #5689); the old copy below does
// not, so the field is left out of what is compared (the time is masked and then dropped).
var concludedRe = regexp.MustCompile(`,?\\*"concluded_at\\*":\\*"<t>\\*"`)

func maskStamps(s string) string {
	return concludedRe.ReplaceAllString(stampRe.ReplaceAllString(s, "<t>"), "")
}

func runVerdictOracle(t *testing.T, seed int64) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	s, err := NewStoreWithKey(pathA, "")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "team", "lead", "Orchestrator", "lead-session", nil, "all")
	require.NoError(t, err)
	workers := []string{"w1", "w2", "w3"}
	reviewers := []string{"r1", "r2", "r3"}
	for _, n := range append(append([]string{}, workers...), reviewers...) {
		_, err = s.Subscribe(ctx, "team", n, n, n+"-session", nil, "all")
		require.NoError(t, err)
	}
	var tasks []int64
	// toReview takes worker's new (or rejected) task through submit, release and delegation to some reviewers. A
	// step the board refuses ends it: it runs on A before the copy, the same for both.
	toReview := func(worker string, id int64) {
		if id == 0 {
			task, err := s.CreateTask(ctx, "team", "work", "", "medium", "lead", worker)
			require.NoError(t, err)
			id = task.ID
			tasks = append(tasks, id)
		}
		if _, err := s.ClaimTask(ctx, "team", worker, id); err != nil {
			return
		}
		if _, err := s.SubmitCompletionReview(ctx, "team", id, worker, "built", "success", "needs review", nil); err != nil {
			return
		}
		if _, err := s.ReleaseCompletionReview(ctx, "team", id, "lead", "review"); err != nil {
			return
		}
		to := append([]string{}, reviewers[:1+rng.Intn(3)]...)
		rng.Shuffle(len(to), func(i, j int) { to[i], to[j] = to[j], to[i] })
		s.DelegateReview(ctx, "team", id, "lead", to)
	}
	for step := 0; step < 25; step++ {
		// More work into review, or a rejected task back in (a new round).
		switch rng.Intn(3) {
		case 0:
			toReview(workers[rng.Intn(len(workers))], 0)
		case 1:
			if len(tasks) > 0 {
				id := tasks[rng.Intn(len(tasks))]
				if task, err := s.GetTask(ctx, "team", id); err == nil && task != nil && task.Status == "pending" && task.AssignedTo != nil {
					toReview(*task.AssignedTo, id)
				}
			}
		}
		if len(tasks) == 0 {
			continue
		}
		// One verdict attempt: by a reviewer mostly, on the task or its review task, for this round or another.
		id := tasks[rng.Intn(len(tasks))]
		if rng.Intn(15) == 0 {
			id = 9999 // not on this board
		}
		round := 0
		if task, err := s.GetTask(ctx, "team", id); err == nil && task != nil {
			rounds := task.Workflow.ReviewRounds
			if n := len(rounds); n > 0 {
				round = []int{0, rounds[n-1].Round, rounds[n-1].Round - 1, rounds[n-1].Round + 1, 0}[rng.Intn(5)] // a stale or unknown round too
				// Sometimes name the reviewer's review task instead of the task.
				if rng.Intn(4) == 0 && len(rounds[n-1].Reviewers) > 0 {
					id = rounds[n-1].Reviewers[rng.Intn(len(rounds[n-1].Reviewers))].ReviewTaskID
				}
			}
		}
		// Now and then a reviewer's own review task is completed, cancelled or given to someone else while its round is open (an Operator or
		// Orchestrator can do each): the verdict is then refused with "is not open", which the flows above alone never reach.
		if rng.Intn(4) == 0 {
			tid := tasks[rng.Intn(len(tasks))]
			if task, err := s.GetTask(ctx, "team", tid); err == nil && task != nil {
				if rs := task.Workflow.ReviewRounds; len(rs) > 0 && rs[len(rs)-1].Result == "" && len(rs[len(rs)-1].Reviewers) > 0 {
					rv := rs[len(rs)-1].Reviewers[rng.Intn(len(rs[len(rs)-1].Reviewers))]
					switch rng.Intn(3) {
					case 0:
						s.CompleteTask(ctx, "team", rv.ReviewTaskID, rv.Reviewer, nil)
					case 1:
						s.CancelTask(ctx, "team", rv.ReviewTaskID, rv.Reviewer, nil)
					case 2:
						s.ReassignTask(ctx, "team", rv.ReviewTaskID, "w1")
					}
				}
			}
		}
		actor := reviewers[rng.Intn(len(reviewers))]
		if rng.Intn(5) == 0 {
			actor = workers[rng.Intn(len(workers))]
		}
		approve := rng.Intn(3) != 0
		note := []string{"", "ok", "not yet: tests fail", "ok", "", strings.Repeat("x", 4100)}[rng.Intn(6)]
		if !approve && note == "" {
			note = "why"
		}

		require.NoError(t, s.Close())
		copyVerdictBoard(t, pathA, pathB)
		var results [2]string
		for i, p := range []string{pathA, pathB} {
			st, err := NewStoreWithKey(p, "")
			require.NoError(t, err)
			if i == 0 {
				results[i] = verdictResult(st.reviewVerdictBefore4599(ctx, "team", id, actor, note, approve, round))
			} else {
				results[i] = verdictResult(st.reviewVerdict(ctx, "team", id, actor, note, approve, round))
			}
			require.NoError(t, st.Close())
		}
		require.Equal(t, results[0], results[1], "step %d: the verdict", step)
		require.Equal(t, dumpVerdictBoard(t, pathA), dumpVerdictBoard(t, pathB), "step %d: the databases after it", step)
		r := results[1]
		switch {
		case strings.HasPrefix(r, "error: "):
			for outcome := range verdictOutcomes {
				if strings.Contains(r, outcome) {
					seenVerdict(outcome)
				}
			}
		case strings.Contains(r, `"Rejected":true`):
			seenVerdict("rejected")
		case strings.Contains(r, `"Complete":true`):
			seenVerdict("approved, round complete")
		default:
			seenVerdict("approved, round still open")
		}
		s, err = NewStoreWithKey(pathA, "")
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
}

// reviewVerdictBefore4599 is reviewVerdict as it was before #4599 (second ranking, item 7).
func (s *Store) reviewVerdictBefore4599(ctx context.Context, project string, id int64, actor, note string, approve bool, reviewed int) (*ReviewVerdict, error) {
	if len(note) > 4096 {
		return nil, fmt.Errorf("the note is too long (4096 characters at most)")
	}
	// id may be the review task: go to its parent.
	if w, err := loadWorkflow(ctx, s.db, id); err == nil && w.ReviewOf > 0 {
		id = w.ReviewOf
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("task #%d is not on this board", id)
		}
		return nil, err
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	n := len(w.ReviewRounds)
	switch {
	case n > 0 && w.ReviewRounds[n-1].Result == "rejected" && status != "review_pending" && status != "completed":
		return nil, fmt.Errorf("task #%d is back with %s (rejected in round %d): give your verdict when it is resubmitted, and your review task reopens", id, w.OriginalAuthor, w.ReviewRounds[n-1].Round)
	case status != "review_pending" || n == 0 || w.ReviewRounds[n-1].Result != "":
		return nil, fmt.Errorf("task #%d has no open review", id)
	}
	round := &w.ReviewRounds[n-1]
	if reviewed != 0 && reviewed != round.Round {
		return nil, fmt.Errorf("you reviewed round %d of task #%d, but it was resubmitted and is at round %d: read it again (coral-board task detail %d) and give your verdict with --round %d", reviewed, id, round.Round, id, round.Round)
	}
	var me *ReviewerState
	for i := range round.Reviewers {
		if round.Reviewers[i].Reviewer == actor {
			me = &round.Reviewers[i]
		}
	}
	if me == nil {
		return nil, fmt.Errorf("only the reviewers task #%d was given to can approve or reject it", id)
	}
	if me.State != "pending" {
		return nil, fmt.Errorf("you already %s task #%d in round %d", me.State, id, round.Round)
	}
	// The verdict goes on the reviewer's own open review task, claimed or not (#3787: any time
	// the task is in review).
	var rtStatus string
	var rtAssignee sql.NullString
	if err := tx.QueryRowxContext(ctx, "SELECT status, assigned_to FROM board_tasks WHERE id=? AND board_id=?", me.ReviewTaskID, project).Scan(&rtStatus, &rtAssignee); err != nil {
		return nil, err
	}
	if (rtStatus != "in_progress" && rtStatus != "pending") || rtAssignee.String != actor {
		return nil, fmt.Errorf("your review task #%d for #%d is not open", me.ReviewTaskID, id)
	}
	now := nowUTC()
	outcome, verdict := "success", "approved"
	if !approve {
		outcome, verdict = "failed", "rejected"
	}
	me.State, me.Note, me.At = verdict, note, now
	msg := fmt.Sprintf("%s task #%d", strings.ToUpper(verdict[:1])+verdict[1:], id)
	if note != "" {
		msg += ": " + note
	}
	if err := finishReviewTask(ctx, tx, project, me.ReviewTaskID, actor, msg, outcome, now); err != nil {
		return nil, err
	}
	v := &ReviewVerdict{Author: w.OriginalAuthor}
	orchAssignee := ""
	_ = tx.GetContext(ctx, &orchAssignee, "SELECT COALESCE(assigned_to,'') FROM board_tasks WHERE id=?", id)
	v.Orchestrator = orchAssignee
	if !approve {
		// The round ends; the other reviewers stay on the task (#3787): their review tasks stay
		// open and reopen for the next round when the author resubmits.
		round.Result = "rejected"
		// Back to its author, pending, with a fresh candidate to come.
		if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status='pending', assigned_to=?, claimed_at=NULL WHERE id=? AND board_id=?", ptr.NullIfEmpty(w.OriginalAuthor), id, project); err != nil {
			return nil, err
		}
		w.CompletionReview = nil
		v.Rejected = true
	} else {
		all := true
		for _, r := range round.Reviewers {
			all = all && r.State == "approved"
		}
		if all {
			round.Result = "approved"
			v.Complete = true
			for _, r := range round.Reviewers {
				v.Approvers = append(v.Approvers, r.Reviewer)
			}
		}
	}
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if v.Task, err = s.GetTask(ctx, project, id); err != nil {
		return nil, err
	}
	v.ReviewTask, err = s.GetTask(ctx, project, me.ReviewTaskID)
	return v, err
}
