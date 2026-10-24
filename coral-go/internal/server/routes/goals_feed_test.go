package routes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #7116: the live feed carries each board's goals brief (open count, change counter) and the totals, with no text: in full in the first update, in a diff only when it changed, and nothing at all while no
// board has a goal.
func goalsTick(goals map[string]board.GoalsBrief) *wsTick {
	t := &wsTick{current: map[string]string{}, byKey: map[string]map[string]any{}, runsJSON: "[]", serversJSON: "null"}
	if len(goals) > 0 {
		b, _ := json.Marshal(goals)
		t.goals, t.goalsJSON = goals, string(b)
		for _, g := range goals {
			t.goalsOpen += g.Open
			if g.Seq > t.goalsSeq {
				t.goalsSeq = g.Seq
			}
		}
	}
	return t
}

func TestFeedCarriesTheGoalsBriefInFullAndInDiffsOnlyWhenItChanged(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	hb := 15 * time.Second
	feed := &coralFeed{prevSessions: map[string]string{}, prevByKey: map[string]map[string]any{}, prevRunsJSON: "[]", prevServersJSON: "null", firstMessage: true}
	send := func(tick *wsTick) (map[string]any, coralFrame) {
		msg, kind := feed.frame(tick, nil, nil, nil, now, hb)
		if msg != nil {
			feed.sent(kind, tick, nil, nil, now)
		}
		return msg, kind
	}

	// No board has a goal: nothing about goals in the update, and no diff for nothing.
	msg, kind := send(goalsTick(nil))
	require.Equal(t, coralUpdate, kind)
	for _, k := range []string{"goals_brief", "goals_open", "goals_seq"} {
		assert.NotContains(t, msg, k)
	}
	msg, _ = send(goalsTick(nil))
	assert.Nil(t, msg, "nothing changed")

	// A goal appears: the diff carries the brief and the totals.
	one := map[string]board.GoalsBrief{"team-a": {Open: 1, Seq: 3}}
	msg, kind = send(goalsTick(one))
	require.Equal(t, coralDiff, kind)
	assert.Equal(t, one, msg["goals_brief"])
	assert.Equal(t, 1, msg["goals_open"])
	assert.EqualValues(t, 3, msg["goals_seq"])
	raw, _ := json.Marshal(msg)
	assert.NotContains(t, string(raw), "title", "no text")

	// The same again: nothing. A change of a counter: a diff again.
	msg, _ = send(goalsTick(one))
	assert.Nil(t, msg)
	two := map[string]board.GoalsBrief{"team-a": {Open: 1, Seq: 4}, "team-b": {Open: 2, Seq: 9}}
	msg, kind = send(goalsTick(two))
	require.Equal(t, coralDiff, kind)
	assert.Equal(t, 3, msg["goals_open"])
	assert.EqualValues(t, 9, msg["goals_seq"], "the largest counter")

	// A new connection's first update has it in full.
	fresh := &coralFeed{prevSessions: map[string]string{}, prevByKey: map[string]map[string]any{}, prevRunsJSON: "[]", prevServersJSON: "null", firstMessage: true}
	full, kind := fresh.frame(goalsTick(two), nil, nil, nil, now, hb)
	require.Equal(t, coralUpdate, kind)
	assert.Equal(t, two, full["goals_brief"])
	assert.Equal(t, 3, full["goals_open"])
}

// The tick is built from the store: a board with goals is in the brief, one that never had any is not, and a write moves the counter.
func TestWSTickBuildsTheGoalsBriefFromTheStore(t *testing.T) {
	ctx := context.Background()
	_, h := setupBoardTestServer(t)
	briefs, err := h.bs.GoalBriefs(ctx)
	require.NoError(t, err)
	assert.Empty(t, briefs, "no goals anywhere")
	g, err := h.bs.CreateGoal(ctx, "team", board.GoalInput{Title: "Ship", CreatedBy: "Operator"})
	require.NoError(t, err)
	_, err = h.bs.CreateGoal(ctx, "other", board.GoalInput{Title: "Later", CreatedBy: "Operator"})
	require.NoError(t, err)
	briefs, err = h.bs.GoalBriefs(ctx)
	require.NoError(t, err)
	require.Contains(t, briefs, "team")
	before := briefs["team"]
	assert.Equal(t, 1, before.Open)
	_, _, err = h.bs.CloseGoal(ctx, "team", g.ID, "done", "")
	require.NoError(t, err)
	briefs, _ = h.bs.GoalBriefs(ctx)
	assert.Equal(t, 0, briefs["team"].Open)
	assert.Greater(t, briefs["team"].Seq, before.Seq, "the counter moved")
	assert.Equal(t, 1, briefs["other"].Open, "the other board is its own")
}
