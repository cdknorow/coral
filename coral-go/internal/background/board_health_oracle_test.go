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

var checkOracleTotals struct{ checks, items, unparked, acked int }

func TestComposeCheckAgreesWithBefore4599(t *testing.T) {
	checkOracleTotals.checks, checkOracleTotals.items, checkOracleTotals.unparked, checkOracleTotals.acked = 0, 0, 0, 0
	for seed := int64(1); seed <= 60; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runCheckOracle(t, seed) })
	}
	t.Logf("compared: %d checks, %d items, %d parks ended, %d acknowledgements", checkOracleTotals.checks, checkOracleTotals.items, checkOracleTotals.unparked, checkOracleTotals.acked)
	require.Greater(t, checkOracleTotals.items, 300)
	require.Greater(t, checkOracleTotals.unparked, 10)
	require.Greater(t, checkOracleTotals.acked, 10)
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
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	bs, err := board.NewStoreWithKey(pathA, "")
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour) // after the database's own stamps
	names := []string{"Orchestrator"}
	for i := 0; i < 2+rng.Intn(5); i++ {
		names = append(names, fmt.Sprintf("Dev %d", i))
	}
	sid := func(name string) string {
		return fmt.Sprintf("%08d-2222-2222-2222-222222222222", len(name)*7+int(name[len(name)-1]))
	}
	for _, name := range names {
		_, err := bs.Subscribe(ctx, "team", name, name, naming.SessionName("claude", sid(name)), nil, "all")
		require.NoError(t, err)
	}
	signals := map[string]agentSignal{}
	known := map[string]bool{}
	var taskIDs []int64
	keysSeen := map[string]bool{}
	lastCheck := map[string]time.Time{}
	for step := 0; step < 10; step++ {
		bySession := map[string]AgentInfo{}
		for _, name := range names {
			if rng.Intn(6) != 0 {
				a := AgentInfo{AgentType: "claude", SessionID: sid(name), DisplayName: name}
				bySession[naming.SessionName(a.AgentType, a.SessionID)] = a
			}
			known[sid(name)] = rng.Intn(5) != 0
			signals[sid(name)] = agentSignal{InTurn: rng.Intn(4) == 0, NeedsInput: rng.Intn(8) == 0,
				Since: now.Add(-time.Duration(rng.Intn(90)) * time.Minute)}
		}
		for k := rng.Intn(3); k > 0; k-- {
			assignee := names[1+rng.Intn(len(names)-1)]
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
			switch rng.Intn(4) {
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
			}
		}
		if rng.Intn(3) == 0 {
			who := names[1+rng.Intn(len(names)-1)]
			if rng.Intn(2) == 0 {
				bs.RegisterWait(ctx, "team", who, naming.SessionName("claude", sid(who)), "operator", "Operator", "may I ship?", time.Hour)
			} else {
				bs.RegisterWait(ctx, "team", who, naming.SessionName("claude", sid(who)), "message", "Orchestrator", "", time.Hour)
			}
		}
		if rng.Intn(2) == 0 {
			from := names[rng.Intn(len(names))]
			bs.PostMessage(ctx, "team", from, "an update on my work", nil)
		}
		// The orchestrator acknowledges some of what it was shown.
		var keys []string
		for k := range keysSeen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if rng.Intn(3) == 0 {
				if bs.AckHealthItem(ctx, "team", teamhealth.ItemID(k), "Orchestrator", now) == nil { // refused for one never shown
					checkOracleTotals.acked++
				}
			}
		}
		now = now.Add(time.Duration(rng.Intn(20)) * time.Minute)

		// Parks that end at this check.
		for _, id := range taskIDs {
			if task, err := bs.GetTask(ctx, "team", id); err == nil && task != nil && task.ParkedReason != nil && !task.Parked(now) && (task.Status == "pending" || task.Status == "in_progress" || task.Status == "blocked" || task.Status == "draft") {
				checkOracleTotals.unparked++
			}
		}
		// The same bytes on both sides.
		require.NoError(t, bs.Close())
		copyBoardFile(t, pathA, pathB)
		stores := make([]*board.Store, 2)
		for i, p := range []string{pathA, pathB} {
			stores[i], err = board.NewStoreWithKey(p, "")
			require.NoError(t, err)
		}
		results := make([]struct {
			text  string
			count int
			err   error
		}, 2)
		for i, s := range stores {
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
		}
		require.Equal(t, results[0], results[1], "step %d: the check", step)
		for _, s := range stores {
			require.NoError(t, s.Close())
		}
		require.Equal(t, dumpBoard(t, pathA), dumpBoard(t, pathB), "step %d: the databases after the check", step)
		checkOracleTotals.checks++
		checkOracleTotals.items += results[1].count
		// The keys shown this time, for the orchestrator to acknowledge later.
		for _, name := range names {
			keysSeen["idle|"+name] = true
		}
		if rng.Intn(4) == 0 {
			lastCheck["team"] = now
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
