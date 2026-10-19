package board

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countUnread, split into its loads, its starts and its per-board count (#4669 part 2, #4599), gives the counts
// countUnreadBefore4599 (the function at v1.4.91, below) gives, and leaves the same sidebar cache behind. Two stores start
// from one seeded file and take the same random changes in step (posts, reads, mode and title changes, leaving and
// rejoining, group changes, deletes, a burst of more than unreadWindow posts), old counting on A, new on B. Each kind of count has a floor, so none goes
// unexercised: receive-all, mentions, an unset mode, a group mode, receive-none, a session on two boards, a count resumed
// from the cache, a read cursor past the board's newest message, and a first count on a board longer than unreadWindow.
//
// The recount when the cache is invalidated during a count needs a delete or group change racing it, which a sequential
// driver never does.

type cuOracleSub struct{ project, id, title, session string }

var cuOracleSubs = []cuOracleSub{
	{"alpha", "Orchestrator", "Orchestrator", "s-orch"},
	{"alpha", "lead", "Lead Developer", "s-lead"},
	{"alpha", "qa2", "QA Engineer 2", "s-qa2"},
	{"alpha", "o'brien", "Developer", "s-ob"},
	{"beta", "lead", "Lead Developer", "s-lead"}, // the same session on a second board
	{"beta", "ops", "DevOps", "s-ops"},
	{"beta", "watcher", "Watcher", "s-watch"},
	{"long", "reader", "Reader", "s-long"},
	{"long", "poster", "Poster", "s-poster"},
}

var cuOracleModes = []string{"all", "mentions", "none", "devs", "ops-team"}

var cuOracleContents = []string{"hello", "@lead please look", "@Lead Developer ping", "@qa2 and @QA Engineer 2", "@o'brien",
	"@Developer one", "@ops", "@DevOps now", "@watcher", "@reader", "@all everyone", "email lead@example.invalid", "@Orchestrator status",
	"@notsubscribed", "'; DROP TABLE board_messages; --", "@leader is not lead"}

func cuSeed(t *testing.T, s *Store, rng *rand.Rand, long bool) {
	t.Helper()
	ctx := context.Background()
	for _, sub := range cuOracleSubs {
		_, err := s.Subscribe(ctx, sub.project, sub.id, sub.title, sub.session, nil, cuOracleModes[rng.Intn(len(cuOracleModes))])
		require.NoError(t, err)
	}
	require.NoError(t, s.AddToGroup(ctx, "alpha", "devs", "lead"))
	require.NoError(t, s.AddToGroup(ctx, "beta", "ops-team", "ops"))
	if long { // more than unreadWindow messages, mentions of reader in the oldest ones too
		tx, err := s.db.Beginx()
		require.NoError(t, err)
		for i := 0; i < unreadWindow+150; i++ {
			content := "filler"
			if i%7 == 0 {
				content = "@reader old news"
			}
			_, err := tx.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES ('long', 'poster', 'poster', ?, '2026-10-01T09:00:00Z')`, content)
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
		_, err = s.db.Exec(`UPDATE board_subscribers SET last_read_id = 0 WHERE project = 'long'`)
		require.NoError(t, err)
	}
}

// cuStep applies one random change to a store; the same rng state gives the same change on both.
func cuStep(t *testing.T, s *Store, r *rand.Rand) {
	ctx := context.Background()
	sub := cuOracleSubs[r.Intn(len(cuOracleSubs))]
	if r.Intn(40) == 0 { // more than unreadWindow new messages: a cached start falls behind the window
		tx, err := s.db.Beginx()
		require.NoError(t, err)
		for i := 0; i < unreadWindow+50; i++ {
			content := []string{"filler", "@reader newer news", "@lead busy board"}[i%3]
			_, err := tx.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES (?, 'poster', 'poster', ?, '2026-10-01T09:00:00Z')`, sub.project, content)
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
		return
	}
	switch r.Intn(12) {
	case 0, 1, 2, 3:
		sender := sub.id
		if r.Intn(8) == 0 {
			sender = []string{"Coral Task Queue", "outsider"}[r.Intn(2)]
		}
		_, err := s.PostMessage(ctx, sub.project, sender, cuOracleContents[r.Intn(len(cuOracleContents))], nil)
		require.NoError(t, err)
	case 4:
		_, err := s.ReadMessages(ctx, sub.project, sub.id, 1+r.Intn(3))
		require.NoError(t, err)
	case 5:
		_, err := s.Subscribe(ctx, sub.project, sub.id, sub.title, sub.session, nil, cuOracleModes[r.Intn(len(cuOracleModes))])
		require.NoError(t, err)
	case 6:
		title := sub.title
		if r.Intn(2) == 0 {
			title += " 2"
		}
		_, err := s.Subscribe(ctx, sub.project, sub.id, title, sub.session, nil, "mentions")
		require.NoError(t, err)
	case 7:
		if r.Intn(2) == 0 {
			_, err := s.Unsubscribe(ctx, sub.project, sub.id)
			require.NoError(t, err)
		} else {
			_, err := s.Subscribe(ctx, sub.project, sub.id, sub.title, sub.session, nil, "")
			require.NoError(t, err)
		}
	case 8:
		group := []string{"devs", "ops-team"}[r.Intn(2)]
		if r.Intn(2) == 0 {
			require.NoError(t, s.AddToGroup(ctx, sub.project, group, sub.id))
		} else {
			_, err := s.RemoveFromGroup(ctx, sub.project, group, sub.id)
			require.NoError(t, err)
		}
	case 9:
		var hi int64
		if r.Intn(2) == 0 { // read to the end, then the board's newest goes: a read cursor past the board's newest message
			_, err := s.ReadMessages(ctx, sub.project, sub.id, 10000)
			require.NoError(t, err)
			require.NoError(t, s.db.Get(&hi, `SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ?`, sub.project))
			if hi > 0 {
				_, err := s.DeleteMessage(ctx, sub.project, hi)
				require.NoError(t, err)
			}
			return
		}
		require.NoError(t, s.db.Get(&hi, `SELECT COALESCE(MAX(id), 0) FROM board_messages`))
		if hi > 0 {
			_, err := s.DeleteMessage(ctx, sub.project, 1+r.Int63n(hi))
			require.NoError(t, err)
		}
	case 10: // an unset mode (older rows), which counts as mentions
		_, err := s.db.Exec(`UPDATE board_subscribers SET receive_mode = '' WHERE project = ? AND subscriber_id = ?`, sub.project, sub.id)
		require.NoError(t, err)
	case 11: // read nothing yet: a first count on the long board looks back unreadWindow messages only
		_, err := s.db.Exec(`UPDATE board_subscribers SET last_read_id = 0 WHERE project = ? AND subscriber_id = ?`, sub.project, sub.id)
		require.NoError(t, err)
	}
}

func cuSessions(rng *rand.Rand) []string {
	switch rng.Intn(6) {
	case 0, 1, 2:
		return nil
	case 3:
		return []string{}
	}
	var out []string
	for _, i := range rng.Perm(len(cuOracleSubs))[:1+rng.Intn(4)] {
		out = append(out, cuOracleSubs[i].session)
	}
	if rng.Intn(3) == 0 {
		out = append(out, "s-unknown")
	}
	return out
}

func sidebarCache(s *Store) map[sidebarUnreadKey]sidebarUnreadEntry {
	s.unreadCounts.mu.Lock()
	defer s.unreadCounts.mu.Unlock()
	out := make(map[sidebarUnreadKey]sidebarUnreadEntry, len(s.unreadCounts.sidebar))
	for k, v := range s.unreadCounts.sidebar {
		out[k] = v
	}
	return out
}

// cuKinds names the kinds of count a call made, from the board as it stood before it and the counts it gave.
func cuKinds(t *testing.T, s *Store, cacheBefore map[sidebarUnreadKey]sidebarUnreadEntry, counts map[string]int) []string {
	var subs []unreadSubscriber
	require.NoError(t, s.db.Select(&subs, `SELECT project, subscriber_id, session_name, job_title, last_read_id, receive_mode FROM board_subscribers WHERE is_active = 1`))
	epoch := s.unreadCounts.epoch.Load()
	perSession := map[string]int{}
	for _, sub := range subs {
		perSession[sub.SessionName]++
	}
	var kinds []string
	for _, sub := range subs {
		if _, asked := counts[sub.SessionName]; !asked {
			continue
		}
		mode := sub.ReceiveMode
		switch {
		case mode == "":
			mode = "unset mode"
		case mode != "all" && mode != "mentions" && mode != "none":
			mode = "group mode"
		}
		if counts[sub.SessionName] > 0 {
			kinds = append(kinds, mode+", counted")
		}
		if mode == "none" && perSession[sub.SessionName] == 1 {
			kinds = append(kinds, fmt.Sprintf("none alone, %d", counts[sub.SessionName]))
		}
		if e, ok := cacheBefore[sidebarUnreadKey{sub.Project, sub.SubscriberID, sub.SessionName}]; ok && e.epoch == epoch && e.lastReadID == sub.LastReadID {
			kinds = append(kinds, "resumed from the cache")
		}
		var hi int64
		require.NoError(t, s.db.Get(&hi, `SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ?`, sub.Project))
		if sub.LastReadID > hi && mode != "none" {
			kinds = append(kinds, "read past the newest")
		}
		if sub.Project == "long" && sub.LastReadID == 0 && sub.ReceiveMode != "all" && sub.ReceiveMode != "none" {
			kinds = append(kinds, "first count on a long board")
		}
	}
	for session, n := range perSession {
		if _, asked := counts[session]; asked && n > 1 && counts[session] > 0 {
			kinds = append(kinds, "a session on two boards")
		}
	}
	return kinds
}

func TestCountUnreadAgreesWithBefore4599(t *testing.T) {
	ctx := context.Background()
	seeds := 5
	if testing.Short() {
		seeds = 2
	}
	// Each seed has its own directory, databases and random source: the seeds run in parallel (#4972), inside one group so
	// the floors are read after every seed has finished.
	var mu sync.Mutex
	kinds := map[string]int{}
	calls := 0
	kind := func(k string) { mu.Lock(); kinds[k]++; mu.Unlock() }
	t.Run("seeds", func(t *testing.T) {
		for seed := int64(1); seed <= int64(seeds); seed++ {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				rng := rand.New(rand.NewSource(seed))
				dir := t.TempDir()
				pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
				seedStore, err := NewStoreWithKey(pathA, "")
				require.NoError(t, err)
				cuSeed(t, seedStore, rng, seed%2 == 0)
				require.NoError(t, seedStore.Close()) // checkpoints the WAL into the main file
				data, err := os.ReadFile(pathA)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(pathB, data, 0o600))
				a, err := NewStoreWithKey(pathA, "")
				require.NoError(t, err)
				b, err := NewStoreWithKey(pathB, "")
				require.NoError(t, err)
				for step := 0; step < 150; step++ {
					stepSeed := rng.Int63()
					cuStep(t, a, rand.New(rand.NewSource(stepSeed)))
					cuStep(t, b, rand.New(rand.NewSource(stepSeed)))
					for k := 1 + rng.Intn(2); k > 0; k-- { // a second count in a row resumes from the cache
						sessions := cuSessions(rng)
						cacheBefore := sidebarCache(b)
						old, oldErr := countUnreadBefore4599(a, ctx, sessions, true)
						got, gotErr := b.countUnread(ctx, sessions, true)
						where := fmt.Sprintf("seed %d step %d: sessions %q", seed, step, sessions)
						require.Equal(t, fmt.Sprint(oldErr), fmt.Sprint(gotErr), where)
						require.Equal(t, old, got, where)
						require.True(t, reflect.DeepEqual(sidebarCache(a), sidebarCache(b)), "%s: the sidebar caches differ", where)
						mu.Lock()
						calls++
						mu.Unlock()
						if gotErr != nil {
							kind("an error")
							continue
						}
						for _, k := range cuKinds(t, b, cacheBefore, got) {
							kind(k)
						}
						if sessions != nil {
							for _, sn := range sessions {
								if _, ok := got[sn]; !ok {
									kind("an unknown session left out")
								}
							}
						}
					}
				}
				require.NoError(t, a.Close())
				require.NoError(t, b.Close())
			})
		}
	})
	t.Logf("%d counts compared; per kind: %v", calls, kinds)
	for _, k := range []string{"all, counted", "mentions, counted", "unset mode, counted", "group mode, counted", "resumed from the cache",
		"first count on a long board", "read past the newest", "a session on two boards", "an unknown session left out", "an error"} {
		assert.GreaterOrEqual(t, kinds[k], 15, "%q went (almost) unexercised", k)
	}
	assert.GreaterOrEqual(t, kinds["none alone, 0"], 15, "a receive-none session alone went (almost) unexercised")
	for k := range kinds {
		assert.False(t, strings.HasPrefix(k, "none alone, ") && k != "none alone, 0", "a receive-none session alone is never counted: %s", k)
	}
}

// countUnreadBefore4599 is countUnread before #4599 split it (store.go at v1.4.91), kept to compare with.
func countUnreadBefore4599(s *Store, ctx context.Context, sessionNames []string, retry bool) (map[string]int, error) {
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
		JobTitle     string `db:"job_title"`
		LastReadID   int64  `db:"last_read_id"`
		ReceiveMode  string `db:"receive_mode"`
	}
	query := "SELECT project, subscriber_id, session_name, job_title, last_read_id, receive_mode FROM board_subscribers WHERE is_active = 1"
	var args []any
	if sessionNames != nil {
		var err error
		query, args, err = sqlx.In(query+" AND session_name IN (?)", sessionNames)
		if err != nil {
			return nil, err
		}
	}
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return map[string]int{}, nil
	}
	projects := make([]string, 0, len(subs))
	seen := make(map[string]bool)
	for _, sub := range subs {
		if !seen[sub.Project] {
			seen[sub.Project] = true
			projects = append(projects, sub.Project)
		}
	}
	var groupRows []struct {
		Project      string `db:"project"`
		GroupID      string `db:"group_id"`
		SubscriberID string `db:"subscriber_id"`
	}
	query, args, err := sqlx.In("SELECT project, group_id, subscriber_id FROM board_groups WHERE project IN (?)", projects)
	if err != nil {
		return nil, err
	}
	if err := s.reader().SelectContext(ctx, &groupRows, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}

	type groupKey struct{ project, groupID string }
	groupsByKey := make(map[groupKey]map[string]bool)
	for _, gr := range groupRows {
		key := groupKey{gr.Project, gr.GroupID}
		if groupsByKey[key] == nil {
			groupsByKey[key] = make(map[string]bool)
		}
		groupsByKey[key][gr.SubscriberID] = true
	}

	// Group subscribers by project. Counts are incremental (see
	// unread_cache.go): each subscriber resumes at its cached watermark, and a
	// project's messages are streamed once from the lowest of them.
	type subInfo struct {
		SubscriberID string
		SessionName  string
		JobTitle     string
		LastReadID   int64
		ReceiveMode  string
		key          sidebarUnreadKey
		start        int64
		count        int
	}
	epoch := s.unreadCounts.epoch.Load()
	result := make(map[string]int)
	byProject := make(map[string][]*subInfo)
	for _, sub := range subs {
		result[sub.SessionName] += 0
		rm := sub.ReceiveMode
		if rm == "none" {
			continue
		}
		if rm == "" {
			rm = "mentions"
		}
		info := &subInfo{SubscriberID: sub.SubscriberID, SessionName: sub.SessionName, JobTitle: sub.JobTitle, LastReadID: sub.LastReadID, ReceiveMode: rm,
			key: sidebarUnreadKey{sub.Project, sub.SubscriberID, sub.SessionName}}
		info.start, info.count = s.unreadCounts.sidebarUnreadStart(info.key, sub.LastReadID, sub.JobTitle, rm, epoch)
		byProject[sub.Project] = append(byProject[sub.Project], info)
	}

	for project, projectSubs := range byProject {
		var hi int64
		if err := s.reader().GetContext(ctx, &hi, "SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ?", project); err != nil {
			return nil, err
		}
		// A subscriber that receives everything is counted by the index: streaming every
		// message's content for it took minutes on a long board (#3875). The others are
		// matched in Go, and a first count (nothing cached) looks back at most unreadWindow
		// messages.
		var streamed []*subInfo
		floor := int64(-1)
		for _, sub := range projectSubs {
			if sub.ReceiveMode == "all" {
				var n int
				if err := s.reader().GetContext(ctx, &n,
					"SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != ? AND subscriber_id != 'Coral Task Queue'",
					project, sub.start, hi, sub.SubscriberID); err != nil {
					return nil, err
				}
				sub.count += n
				continue
			}
			if sub.start == sub.LastReadID {
				if floor < 0 {
					floor = s.unreadFloor(ctx, project)
				}
				sub.start = max(sub.start, floor)
			}
			streamed = append(streamed, sub)
		}
		if len(streamed) > 0 {
			minStart := streamed[0].start
			for _, sub := range streamed {
				minStart = min(minStart, sub.start)
			}
			numbered := s.numberedNamesCached(ctx, project)
			// Stream messages instead of retaining every unread body in memory.
			rows, err := s.reader().QueryxContext(ctx,
				"SELECT id, subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != 'Coral Task Queue' ORDER BY id",
				project, minStart, hi)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id int64
				var sender, content string
				if err := rows.Scan(&id, &sender, &content); err != nil {
					rows.Close()
					return nil, err
				}
				for _, sub := range streamed {
					if id <= sub.start || sender == sub.SubscriberID {
						continue
					}
					eligible := false
					switch sub.ReceiveMode {
					case "mentions":
						eligible = containsExplicitTag(content, sub.SubscriberID, sub.JobTitle, numbered)
					default:
						eligible = groupsByKey[groupKey{project, sub.ReceiveMode}][sender]
					}
					if eligible {
						sub.count++
					}
				}
			}
			readErr := rows.Err()
			closeErr := rows.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		for _, sub := range projectSubs {
			result[sub.SessionName] += sub.count
			s.unreadCounts.storeSidebarUnread(sub.key, sidebarUnreadEntry{lastReadID: sub.LastReadID, jobTitle: sub.JobTitle, receiveMode: sub.ReceiveMode,
				scannedTo: max(hi, sub.start), count: sub.count, epoch: epoch})
		}
	}
	if retry && s.unreadCounts.epoch.Load() != epoch {
		// Messages were deleted or groups changed meanwhile: cached bases may
		// be stale, so count again.
		return countUnreadBefore4599(s, ctx, sessionNames, false)
	}
	return result, nil
}
