package background

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// composeCheck was split for #4599 (second ranking, item 5). The check is what the orchestrator acts on, so the old
// function is kept below (verbatim but for its name). Before every step board A's database is copied to B, so both
// start from the same bytes (timestamps included); then the old check runs on A and the new one on B, and the text,
// the item count and every table of the two databases afterwards (the parks cleared, the items noted) must be equal.

// checkPhrases: what each section of the check prints, counted over every check; each has a floor.
var checkPhrases = map[string]int{
	"has not claimed #": 20, "silent on #": 20, "idle ": 30, "reviews waiting": 10, "Board": 0,
	"Since ": 30, "No progress since": 30, "awake agents are working": 5, "Nothing needs you": 5,
	"Waiting on the operator": 20, "(back": 10, "parked": 10, "Needs you,": 30, "Waiting on ": 20,
}

var checkSeen map[string]int

func TestComposeCheckAgreesWithBefore4599(t *testing.T) {
	checkSeen = map[string]int{}
	for seed := int64(1); seed <= 120; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runCheckOracle(t, seed) })
	}
	t.Logf("compared, per phrase: %v", checkSeen)
	for phrase, floor := range checkPhrases {
		require.GreaterOrEqual(t, checkSeen[phrase], floor, "%q went (almost) unprinted", phrase)
	}
}

func copyBoardFile(t *testing.T, from, to string) {
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

// dumpBoard is every row of every table, as text, sorted.
func dumpBoard(t *testing.T, path string) []string {
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
			out = append(out, b.String())
		}
		r.Close()
	}
	sort.Strings(out)
	return out
}

func runCheckOracle(t *testing.T, seed int64) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	calm := seed%8 == 0 // everyone working, nothing to judge: the all-clear lines
	// The clock is read once, and every step's time is it plus whole minutes plus 30 s: the database stamps to the
	// second, moments after it, so no comparison with a whole-minute window lands within seconds of its boundary,
	// and the same seed gives the same check on every run.
	base := time.Now().UTC()
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	bs, err := board.NewStoreWithKey(pathA, "")
	require.NoError(t, err)
	devs := []string{}
	for i := 0; i < 2+rng.Intn(4); i++ {
		devs = append(devs, fmt.Sprintf("Dev %d", i))
	}
	reviewers := []string{"Rev 1", "Rev 2"}
	names := append(append([]string{"Orchestrator"}, devs...), reviewers...)
	sid := func(name string) string {
		return fmt.Sprintf("%08d-2222-2222-2222-222222222222", len(name)*7+int(name[len(name)-1]))
	}
	for _, name := range names {
		_, err := bs.Subscribe(ctx, "team", name, name, naming.SessionName("claude", sid(name)), nil, "all")
		require.NoError(t, err)
	}
	// review takes a new task of dev's through submit and delegation to reviewers (each gets a review task). A step
	// the board refuses (dev has a task in progress already) ends it there; it runs on board A before the copy, so
	// both sides see the same result.
	review := func(dev string, to ...string) {
		task, err := bs.CreateTask(ctx, "team", "work", "", "medium", "Orchestrator", dev)
		require.NoError(t, err)
		if _, err = bs.ClaimTask(ctx, "team", dev, task.ID); err != nil {
			return
		}
		if _, err = bs.SubmitCompletionReview(ctx, "team", task.ID, dev, "built", "success", "needs review", nil); err != nil {
			return
		}
		if _, err = bs.ReleaseCompletionReview(ctx, "team", task.ID, "Orchestrator", "review"); err != nil {
			return
		}
		bs.DelegateReview(ctx, "team", task.ID, "Orchestrator", to)
	}
	// Rev 2 has reviewed before (so it is a reviewer) and is done: idle with no work, unless it gets more.
	review(devs[0], "Rev 2")
	_, err = bs.ApproveReview(ctx, "team", 0+1, "Rev 2", "ok", 0)
	_ = err // the id is the first task's; a refusal only leaves Rev 2 with work, the same on both sides
	if calm {
		for _, d := range devs {
			task, err := bs.CreateTask(ctx, "team", "work", "", "medium", "Orchestrator", d)
			require.NoError(t, err)
			bs.ClaimTask(ctx, "team", d, task.ID) // refused for one already working: still working
		}
	}
	signals := map[string]agentSignal{}
	known := map[string]bool{}
	var taskIDs []int64
	keysSeen := map[string]bool{}
	lastCheck := map[string]time.Time{}
	for step := 0; step < 10; step++ {
		// The database's own clock, a few minutes on: its stamps (posts, claims, completions) fall after the
		// last check or before it, as the check's progress line needs both.
		now := base.Add(30*time.Second + time.Duration(step+rng.Intn(5))*time.Minute)
		bySession := map[string]AgentInfo{}
		for _, name := range names {
			if calm || rng.Intn(6) != 0 {
				a := AgentInfo{AgentType: "claude", SessionID: sid(name), DisplayName: name}
				bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
			}
			known[sid(name)] = calm || rng.Intn(5) != 0
			signals[sid(name)] = agentSignal{InTurn: calm || rng.Intn(4) == 0, NeedsInput: !calm && rng.Intn(8) == 0,
				Since: now.Add(-time.Duration(rng.Intn(90)) * time.Minute)}
		}
		if !calm {
			signals[sid("Rev 2")] = agentSignal{Since: now.Add(-45 * time.Minute)} // idle: the review rule's other reviewer
			for k := rng.Intn(3); k > 0; k-- {
				assignee := devs[rng.Intn(len(devs))]
				if rng.Intn(4) == 0 {
					assignee = ""
				}
				task, err := bs.CreateTask(ctx, "team", "work", "", []string{"high", "medium", "low"}[rng.Intn(3)], "Operator", assignee)
				require.NoError(t, err)
				taskIDs = append(taskIDs, task.ID)
			}
			for k := rng.Intn(3); k > 0 && len(taskIDs) > 0; k-- {
				id := taskIDs[rng.Intn(len(taskIDs))]
				task, err := bs.GetTask(ctx, "team", id)
				if err != nil || task == nil {
					continue
				}
				switch rng.Intn(5) {
				case 0:
					if task.AssignedTo != nil {
						bs.ClaimTask(ctx, "team", *task.AssignedTo, id)
					}
				case 1: // parked until a moment already past, or still ahead
					bs.ParkTask(ctx, "team", id, "a long test", now.Add(time.Duration(rng.Intn(60)-30)*time.Minute))
				case 2:
					if task.AssignedTo != nil {
						rule := []string{ruleClaimNudge, ruleStatusNudge}[rng.Intn(2)]
						bs.BumpHealthCounter(ctx, "team", rule, *task.AssignedTo, fmt.Sprint(id), now.Add(-time.Hour))
						bs.MarkHealthEscalated(ctx, "team", rule, *task.AssignedTo, fmt.Sprint(id), now.Add(-time.Duration(rng.Intn(120))*time.Minute))
						keysSeen["escalation|"+rule+"|"+*task.AssignedTo+"|"+fmt.Sprint(id)] = true
					}
				case 3:
					bs.CancelTask(ctx, "team", id, "Operator", nil)
				case 4:
					if task.AssignedTo != nil {
						bs.CompleteTask(ctx, "team", id, *task.AssignedTo, nil)
					}
				}
			}
			// Reviews: mostly to Rev 1, whose queue grows; some approved or rejected.
			if rng.Intn(2) == 0 {
				to := "Rev 1"
				if rng.Intn(5) == 0 {
					to = "Rev 2"
				}
				review(devs[rng.Intn(len(devs))], to)
			}
			if open, _ := bs.OpenReviews(ctx, "team"); len(open["Rev 1"]) > 0 && rng.Intn(4) == 0 {
				id := open["Rev 1"][rng.Intn(len(open["Rev 1"]))]
				if rng.Intn(2) == 0 {
					bs.ApproveReview(ctx, "team", id, "Rev 1", "ok", 0)
				} else {
					bs.RejectReview(ctx, "team", id, "Rev 1", "not yet", 0)
				}
			}
			if rng.Intn(3) == 0 {
				who := devs[rng.Intn(len(devs))]
				if rng.Intn(2) == 0 {
					bs.RegisterWait(ctx, "team", who, naming.SessionName("claude", sid(who)), "operator", "Operator", "may I ship?", time.Hour)
				} else {
					bs.RegisterWait(ctx, "team", who, naming.SessionName("claude", sid(who)), "message", "Orchestrator", "", time.Hour)
				}
			}
			var keys []string
			for k := range keysSeen {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if rng.Intn(3) == 0 {
					bs.AckHealthItem(ctx, "team", teamhealth.ItemID(k), "Orchestrator", now) // refused for one never shown
				}
			}
		}
		if rng.Intn(2) == 0 {
			bs.PostMessage(ctx, "team", names[rng.Intn(len(names))], "an update on my work", nil)
		}
		// The last check: none (the check counts from an interval ago), or just ahead of this step's activity.
		switch rng.Intn(3) {
		case 0:
			delete(lastCheck, "team")
		case 1:
			lastCheck["team"] = now.Add(time.Minute)
		}

		// The same bytes on both sides.
		require.NoError(t, bs.Close())
		copyBoardFile(t, pathA, pathB)
		var results [2]struct {
			text  string
			count int
			err   error
		}
		for i, p := range []string{pathA, pathB} {
			s, err := board.NewStoreWithKey(p, "")
			require.NoError(t, err)
			m := &BoardHealthMonitor{store: s, interval: 10 * time.Minute, idleAfter: 30 * time.Minute, now: func() time.Time { return now },
				signal:    func(_ context.Context, id string) (agentSignal, bool) { return signals[id], known[id] },
				lastCheck: map[string]time.Time{}}
			for k, v := range lastCheck {
				m.lastCheck[k] = v
			}
			rcpt, err := s.GetProjectSubscription(ctx, "team", "Orchestrator")
			require.NoError(t, err)
			if i == 0 {
				results[i].text, results[i].count, results[i].err = m.composeCheckBefore4599(ctx, "team", "Orchestrator", rcpt, bySession)
			} else {
				results[i].text, results[i].count, results[i].err = m.composeCheck(ctx, "team", "Orchestrator", rcpt, bySession)
			}
			require.NoError(t, s.Close())
		}
		require.Equal(t, results[0], results[1], "step %d: the check", step)
		require.Equal(t, dumpBoard(t, pathA), dumpBoard(t, pathB), "step %d: the databases after the check", step)
		for phrase := range checkPhrases {
			checkSeen[phrase] += strings.Count(results[1].text, phrase)
		}
		for _, name := range names {
			keysSeen["idle|"+name] = true
			keysSeen["reviews|"+name] = true
		}
		bs, err = board.NewStoreWithKey(pathA, "")
		require.NoError(t, err)
	}
	require.NoError(t, bs.Close())
}

func TestReviewItems(t *testing.T) {
	reviews := map[string][]int64{"QA": {1, 2, 3, 4}, "Sec": {5, 6}, "Lead": {7, 8, 9}}
	reviewers := map[string]bool{"QA": true, "Sec": true, "Lead": true}
	items := reviewItems(reviews, reviewers, map[string]bool{"9": true}, map[string]bool{"Sec": true})
	require.Len(t, items, 1, "QA has four while Sec is idle; Lead's third is parked")
	assert.Equal(t, "4 reviews waiting (#1, #2, #3, ...) while Sec is idle", items[0].Problem)
	assert.Equal(t, "move one to Sec", items[0].Suggest)
	assert.Empty(t, reviewItems(map[string][]int64{"QA": {1, 2, 3}}, reviewers, nil, map[string]bool{"QA": true}), "the idle one must be another reviewer")
	assert.Empty(t, reviewItems(map[string][]int64{"QA": {1, 2, 3}}, reviewers, nil, map[string]bool{"Bob": true}), "an idle non-reviewer does not count")
	assert.Len(t, reviewItems(map[string][]int64{"QA": {1, 2, 3}}, reviewers, nil, map[string]bool{"Lead": true}), 1, "three is enough")
}

// composeCheckBefore4599 is composeCheck as it was before #4599 (second ranking, item 5).
func (m *BoardHealthMonitor) composeCheckBefore4599(ctx context.Context, project, orch string, rcpt *board.Subscriber, bySession map[string]AgentInfo) (string, int, error) {
	now := m.now()
	subs, err := m.store.ListSubscribers(ctx, project)
	if err != nil {
		return "", 0, err
	}
	all, err := m.store.ListOpenTasks(ctx, project)
	if err != nil {
		return "", 0, err
	}
	// A parked task raises no item (#4175 step 3): it is left out of everything below.
	var tasks []board.Task
	parked := map[string]bool{}
	var backFromPark []int64
	for _, t := range all {
		if t.Parked(now) {
			parked[fmt.Sprint(t.ID)] = true
			continue
		}
		// Its end passed: back, said once in this check, and the park cleared now. Cleared before
		// the check is delivered, so a check that then fails to go out loses this note (a notice
		// only: the task itself is back on the list either way).
		if t.ParkedReason != nil {
			backFromPark = append(backFromPark, t.ID)
			_, _ = m.store.UnparkTask(ctx, project, t.ID)
		}
		tasks = append(tasks, t)
	}
	waits, _ := m.store.ListActiveWaits(ctx, project)
	waiting := map[string]bool{}
	for _, w := range waits {
		waiting[w.SubscriberID] = true
	}
	var items []teamhealth.Item
	said := func(name string) string {
		content, _, err := m.store.LastPostBy(ctx, project, name)
		if err != nil || content == "" {
			return ""
		}
		return teamhealth.ShownText(content, nil)
	}

	// 1. What Coral's own rules escalated.
	if esc, err := m.store.HealthEscalations(ctx, project); err == nil {
		for _, e := range esc {
			if parked[e.Subject] {
				continue
			}
			at, _ := time.Parse("2006-01-02T15:04:05.000Z", e.At)
			ago := teamhealth.Age(now.Sub(at))
			key := "escalation|" + e.Rule + "|" + e.Target + "|" + e.Subject
			switch e.Rule {
			case ruleClaimNudge:
				items = append(items, teamhealth.Item{Rank: 1, Agent: teamhealth.Name(e.Target),
					Problem: fmt.Sprintf("has not claimed #%s after Coral's claim nudges (escalated %s ago)", e.Subject, ago),
					Suggest: fmt.Sprintf("ask it, or reassign #%s", e.Subject), Key: key, State: e.At})
			case ruleStatusNudge:
				items = append(items, teamhealth.Item{Rank: 1, Agent: teamhealth.Name(e.Target),
					Problem: fmt.Sprintf("silent on #%s since Coral asked for a status (escalated %s ago)", e.Subject, ago),
					Said:    said(e.Target), Suggest: fmt.Sprintf("check on it, or reassign #%s", e.Subject), Key: key, State: e.At})
			}
		}
	}

	// 2. An agent with nothing in progress and nothing assigned, idle: it needs work.
	busy := map[string]bool{}
	for _, t := range tasks {
		if t.AssignedTo != nil && (t.Status == "in_progress" || t.Status == "pending") {
			busy[*t.AssignedTo] = true
		}
	}
	reviews, _ := m.store.OpenReviews(ctx, project)
	reviewers, _ := m.store.Reviewers(ctx, project)
	agents := 0
	idleNoWork := map[string]bool{}
	for _, sub := range subs {
		a, live := bySession[sub.SessionName]
		if !live {
			continue
		}
		agents++
		if board.SameIdentity(sub.SubscriberID, orch) || busy[sub.SubscriberID] || waiting[sub.SubscriberID] {
			continue
		}
		sig, known := m.signal(ctx, a.SessionID)
		if !known || sig.InTurn || sig.NeedsInput || sig.Since.IsZero() || now.Sub(sig.Since) < m.idleAfter {
			continue
		}
		idleNoWork[sub.SubscriberID] = true
		items = append(items, teamhealth.Item{Rank: 2, Agent: teamhealth.Name(sub.SubscriberID),
			Problem: fmt.Sprintf("idle %s with nothing in progress or assigned", teamhealth.Age(now.Sub(sig.Since))),
			Suggest: "assign it the next task", Key: "idle|" + sub.SubscriberID, State: sig.Since.UTC().Format(time.RFC3339)})
	}

	// 3. A reviewer with three or more reviews waiting while another reviewer is idle.
	var names []string
	for r := range reviews {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, r := range names {
		var open []int64
		for _, id := range reviews[r] {
			if !parked[fmt.Sprint(id)] {
				open = append(open, id)
			}
		}
		reviews[r] = open
		if len(reviews[r]) < 3 {
			continue
		}
		for other := range reviewers {
			if other != r && idleNoWork[other] {
				items = append(items, teamhealth.Item{Rank: 3, Agent: teamhealth.Name(r),
					Problem: fmt.Sprintf("%d reviews waiting (%s) while %s is idle", len(reviews[r]), taskList(reviews[r]), teamhealth.Name(other)),
					Suggest: fmt.Sprintf("move one to %s", teamhealth.Name(other)), Key: "reviews|" + r, State: fmt.Sprint(reviews[r])})
				break
			}
		}
	}

	// 4. Queue problems (task numbers only).
	issues, _, _ := taskQueueIssues(tasks)
	for _, is := range issues {
		items = append(items, teamhealth.Item{Rank: 4, Agent: "Board", Problem: is, Key: "queue|" + is, State: "shown"})
	}

	// What the orchestrator acknowledged stays off the list while its state is the same (#4175
	// step 3): the states shown are recorded, and acknowledged ones left out.
	states := map[string]string{}
	for _, it := range items {
		if it.Key != "" {
			states[teamhealth.ItemID(it.Key)] = it.State
		}
	}
	if acked, back, err := m.store.NoteHealthItems(ctx, project, states, now); err == nil {
		kept := items[:0]
		for _, it := range items {
			if it.Key == "" || !acked[teamhealth.ItemID(it.Key)] {
				it.Back = it.Key != "" && back[teamhealth.ItemID(it.Key)]
				kept = append(kept, it)
			}
		}
		items = kept
	}

	m.mu.Lock()
	since, ok := m.lastCheck[project]
	m.mu.Unlock()
	if !ok {
		since = now.Add(-m.interval)
	}
	p, _ := m.store.ProgressSince(ctx, project, since)
	var questions []teamhealth.Question
	if qs, err := m.store.ListOperatorQuestions(ctx, project); err == nil {
		for _, q := range qs {
			at, err := time.Parse(time.RFC3339, q.CreatedAt)
			if err != nil {
				continue
			}
			questions = append(questions, teamhealth.Question{Asker: teamhealth.Name(q.SubscriberID),
				Text: teamhealth.ShownText(q.Reason, nil), Since: at})
		}
	}
	roster, attention := m.buildRoster(ctx, rosterInputs{project: project, recipient: rcpt, subs: subs, bySession: bySession, waits: waits, tasks: tasks, now: now})
	return teamhealth.Compose(teamhealth.CheckInput{Now: now, Agents: agents, Items: items, Questions: questions,
		Progress: teamhealth.Progress{Since: since, Completed: p.Completed, Reviewed: p.Reviewed, Posts: p.Posts},
		Parked:   len(parked), BackFromPark: backFromPark, Roster: roster}), len(items) + attention, nil
}
