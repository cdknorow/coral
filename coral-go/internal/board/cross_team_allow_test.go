package board

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAllowStore(t *testing.T) *Store { return testStore(t) }

// #5508: every board row has a generated, unique, never-reissued cross-team id, whichever statement inserted it.
func TestEveryBoardRowGetsACrossTeamIDWhicheverInsertMadeIt(t *testing.T) {
	ctx := context.Background()
	bs := newAllowStore(t)
	_, err := bs.Subscribe(ctx, "alpha", "Orchestrator", "Orchestrator", "tmux-a", nil, "mentions") // ensureBoardRow: INSERT OR IGNORE (id = label = name)
	require.NoError(t, err)
	b, err := bs.CreateBoard(ctx, "created board", "operator") // the create path
	require.NoError(t, err)
	// A plain insert, as a test (or an import) would make: the trigger fills the id.
	_, err = bs.db.ExecContext(ctx, `INSERT INTO boards (id, label, created_at) VALUES ('plain', 'plain', '2026-10-20T00:00:00Z')`)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, key := range []string{"alpha", b.ID, "plain"} {
		id, ok, err := bs.CrossTeamID(ctx, key)
		require.NoError(t, err)
		require.True(t, ok, key)
		assert.Regexp(t, `^x-[a-z2-7]{16}$`, id, key)
		assert.False(t, seen[id], "unique")
		seen[id] = true
	}
	// A row with no id is nobody's sender: not found, never equal to anything.
	_, err = bs.db.ExecContext(ctx, `DROP TRIGGER boards_cross_team_id`)
	require.NoError(t, err)
	_, err = bs.db.ExecContext(ctx, `INSERT INTO boards (id, label, created_at) VALUES ('nullid', 'nullid', '2026-10-20T00:00:00Z')`)
	require.NoError(t, err)
	_, ok, err := bs.CrossTeamID(ctx, "nullid")
	require.NoError(t, err)
	assert.False(t, ok, "a NULL id is not an id")
	_, ok, err = bs.CrossTeamID(ctx, "no such board")
	require.NoError(t, err)
	assert.False(t, ok)
}

// A deleted board's id goes with its row; a re-created board of the same name is new.
func TestARecreatedBoardGetsANewCrossTeamID(t *testing.T) {
	ctx := context.Background()
	bs := newAllowStore(t)
	_, err := bs.Subscribe(ctx, "alpha", "Orchestrator", "Orchestrator", "tmux-a", nil, "mentions")
	require.NoError(t, err)
	old, _, _ := bs.CrossTeamID(ctx, "alpha")
	_, err = bs.db.ExecContext(ctx, `DELETE FROM boards WHERE id = 'alpha'`)
	require.NoError(t, err)
	bs.ensureBoardRow(ctx, "alpha")
	fresh, ok, err := bs.CrossTeamID(ctx, "alpha")
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, old, fresh, "same name, new board, new id")
	_, _, found, _ := bs.BoardByCrossTeamID(ctx, old)
	assert.False(t, found, "the old id names nothing")
}

func TestAllowsSenderNeedsBothTheServerAndTheID(t *testing.T) {
	a, b := "x-"+strings.Repeat("a", 16), "x-"+strings.Repeat("b", 16)
	set := BoardSettings{CrossTeamAllowed: []AllowedSender{{Server: "hub-1", CrossTeamID: a}, {Server: LocalServer, CrossTeamID: b}}}
	assert.True(t, set.AllowsSender("hub-1", a))
	assert.True(t, set.AllowsSender(LocalServer, b))
	assert.False(t, set.AllowsSender("hub-2", a), "the right id on the wrong server")
	assert.False(t, set.AllowsSender("hub-1", b), "the right server, another board's id")
	assert.False(t, set.AllowsSender(LocalServer, a), "an entry for a hub server is not a local one")
	assert.False(t, set.AllowsSender("", a))
	assert.False(t, set.AllowsSender("hub-1", ""))
	assert.False(t, set.AllowsSender("hub-1", "alpha"), "a name is not an id")
	assert.False(t, set.AllowsSender("hub-1", strings.ToUpper(a)))
	assert.False(t, BoardSettings{}.AllowsSender("hub-1", a), "an empty list admits nobody")
}

func TestAllowedProblemChecksTheWholeList(t *testing.T) {
	a, b := "x-"+strings.Repeat("a", 16), "x-"+strings.Repeat("b", 16)
	out, _, _, _ := AllowedProblem([]AllowedSender{{Server: " HUB-1 ", CrossTeamID: a}, {Server: "local", CrossTeamID: b}}, "x-"+strings.Repeat("c", 16))
	assert.Equal(t, []AllowedSender{{Server: "hub-1", CrossTeamID: a}, {Server: "local", CrossTeamID: b}}, out)
	for name, c := range map[string]struct {
		list  []AllowedSender
		row   int
		field string
	}{
		"bad server":    {[]AllowedSender{{Server: "no way!", CrossTeamID: a}}, 0, "server"},
		"empty server":  {[]AllowedSender{{Server: "", CrossTeamID: a}}, 0, "server"},
		"a name":        {[]AllowedSender{{Server: "local", CrossTeamID: "alpha"}}, 0, "cross_team_id"},
		"id of a hub x": {[]AllowedSender{{Server: "local", CrossTeamID: a}, {Server: "local", CrossTeamID: "x-short"}}, 1, "cross_team_id"},
		"twice":         {[]AllowedSender{{Server: "local", CrossTeamID: a}, {Server: "local", CrossTeamID: a}}, 1, "cross_team_id"},
		"itself, local": {[]AllowedSender{{Server: "local", CrossTeamID: "x-" + strings.Repeat("c", 16)}}, 0, "cross_team_id"},
	} {
		_, row, field, msg := AllowedProblem(c.list, "x-"+strings.Repeat("c", 16))
		assert.Equal(t, c.row, row, name)
		assert.Equal(t, c.field, field, name)
		assert.NotEmpty(t, msg, name)
	}
	// The same id on ANOTHER Coral is not "itself".
	_, _, field, _ := AllowedProblem([]AllowedSender{{Server: "hub-1", CrossTeamID: "x-" + strings.Repeat("c", 16)}}, "x-"+strings.Repeat("c", 16))
	assert.Empty(t, field)
	var many []AllowedSender
	for i := 0; i <= MaxAllowedSenders; i++ {
		many = append(many, AllowedSender{Server: "local", CrossTeamID: "x-" + strings.Repeat(string(rune('a'+i%26)), 8) + strings.Repeat(string(rune('a'+(i/26)%26)), 8)})
	}
	_, _, field, _ = AllowedProblem(many[:MaxAllowedSenders], "")
	assert.Empty(t, field, "200 is the limit")
	_, _, field, _ = AllowedProblem(many, "")
	assert.Equal(t, "cross_team_allowed", field)
}

func TestTheListIsStoredWholeAndReadFresh(t *testing.T) {
	ctx := context.Background()
	bs := newAllowStore(t)
	a := "x-" + strings.Repeat("a", 16)
	list := []AllowedSender{{Server: "hub-1", CrossTeamID: a}}
	set, err := bs.UpdateBoardSettings(ctx, "beta", BoardSettingsUpdate{CrossTeamAllowed: &list})
	require.NoError(t, err)
	assert.Equal(t, list, set.CrossTeamAllowed)
	set, err = bs.GetBoardSettings(ctx, "beta")
	require.NoError(t, err)
	assert.True(t, set.AllowsSender("hub-1", a))
	empty := []AllowedSender{}
	set, err = bs.UpdateBoardSettings(ctx, "beta", BoardSettingsUpdate{CrossTeamAllowed: &empty})
	require.NoError(t, err)
	assert.False(t, set.AllowsSender("hub-1", a), "removed: the very next read says no")
}

func TestSettingsLogKeepsTheLastFiveHundredAndBoundsTheHourlyRow(t *testing.T) {
	ctx := context.Background()
	bs := newAllowStore(t)
	for i := 0; i < maxLogRows+20; i++ {
		require.NoError(t, bs.AppendEditLog(ctx, "beta", "Operator", "cross_team_allowed", "[]", "[...]"))
	}
	log, err := bs.ListLog(ctx, "beta")
	require.NoError(t, err)
	assert.Len(t, log, maxLogRows)
	assert.Equal(t, LogEdit, log[0].Kind)
	assert.Equal(t, "Operator", log[0].Actor)

	now := time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)
	for i := 0; i < 150; i++ { // 150 distinct senders in one hour: ONE row, 20 named, the rest only counted
		s := AllowedSender{Server: "hub-1", CrossTeamID: "x-" + strings.Repeat(string(rune('a'+i%26)), 8) + strings.Repeat(string(rune('a'+i/26)), 8)}
		require.NoError(t, bs.NoteHourly(ctx, "gamma", LogRefused, s, now))
	}
	require.NoError(t, bs.NoteHourly(ctx, "gamma", LogWouldRefuse, AllowedSender{Server: "hub-1", CrossTeamID: "x-" + strings.Repeat("z", 16)}, now))
	require.NoError(t, bs.NoteHourly(ctx, "gamma", LogRefused, AllowedSender{}, now.Add(time.Hour))) // the next hour: a new row
	log, err = bs.ListLog(ctx, "gamma")
	require.NoError(t, err)
	var refused, would []LogEntry
	for _, e := range log {
		switch e.Kind {
		case LogRefused:
			refused = append(refused, e)
		case LogWouldRefuse:
			would = append(would, e)
		}
	}
	require.Len(t, refused, 2, "one row per hour")
	require.Len(t, would, 1, "shadow rows are their own kind")
	first := refused[1]
	assert.Equal(t, 150, first.Count)
	assert.Len(t, first.Senders, maxLoggedSenders)
	assert.Equal(t, 130, first.Others)
}

func TestCleanClaimedLabelIsPlainAndBounded(t *testing.T) {
	assert.Equal(t, "ops team", CleanClaimedLabel("ops\x1b[31m \u202e team\u200b"))
	assert.Len(t, []rune(CleanClaimedLabel(strings.Repeat("a", 500))), maxClaimedLabel)
}

// The owner's suggestions come from the stored envelopes of the last 30 days: by sender id, the label is the sender's claim (cleaned), a sender from before ids cannot be listed.
func TestRecentSendersAreSuggestedByIDWithCleanedLabelsAndNoContent(t *testing.T) {
	ctx := context.Background()
	bs := testStore(t)
	_, err := bs.Subscribe(ctx, "beta", "Orchestrator", "Orchestrator", "tmux-b", nil, "mentions")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "alpha", "Orchestrator", "Orchestrator", "tmux-a", nil, "mentions")
	require.NoError(t, err)
	alphaID, _, _ := bs.CrossTeamID(ctx, "alpha")
	remote := "x-" + strings.Repeat("r", 16)
	post := func(o MessageOrigin, content string) {
		o.Direction, o.TargetServerID, o.TargetBoard = "inbound", "local", "beta"
		_, _, err := bs.PostRelayedCrossTeamMessage(ctx, CrossTeamPost{Origin: o, Mentions: []string{"Orchestrator"}, Content: content})
		require.NoError(t, err)
	}
	n := 0
	rid := func() string { n++; return strings.Repeat("m", n) }
	post(MessageOrigin{ServerID: "hub-1", Board: "ops\x1b[31m \u202eteam", Sender: "Orchestrator", FromCrossTeamID: remote, RelayMessageID: rid()}, "SECRET CONTENT ONE")
	post(MessageOrigin{ServerID: "hub-1", Board: "ops team", Sender: "Orchestrator", FromCrossTeamID: remote, RelayMessageID: rid()}, "SECRET CONTENT TWO")
	post(MessageOrigin{ServerID: "hub-2", Board: "old coral", Sender: "Orchestrator", RelayMessageID: rid()}, "from before ids")
	_, _, err = bs.PostCrossTeamMessage(ctx, CrossTeamPost{Origin: MessageOrigin{ServerID: "local", Board: "alpha", Sender: "Orchestrator", SenderRole: "Orchestrator", TargetServerID: "local", TargetBoard: "beta"},
		Mentions: []string{"Orchestrator"}, Content: "local, no id in the envelope"})
	require.NoError(t, err)

	seen, err := bs.RecentSenders(ctx, "beta", time.Now().Add(-30*24*time.Hour))
	require.NoError(t, err)
	byKey := map[string]SeenSender{}
	for _, s := range seen {
		byKey[s.Server+"|"+s.CrossTeamID+"|"+s.Label] = s
	}
	r := byKey["hub-1|"+remote+"|ops team"]
	assert.Equal(t, 2, r.Count, "counted by sender id")
	assert.Equal(t, "ops team", r.Label, "the claimed label, cleaned")
	old := byKey["hub-2||old coral"]
	assert.Equal(t, 1, old.Count)
	assert.Empty(t, old.CrossTeamID, "a sender from before ids has no id to list")
	local := byKey["local|"+alphaID+"|alpha"]
	assert.Equal(t, 1, local.Count, "a local sender's id is read from its own row")
	raw, _ := json.Marshal(seen)
	assert.NotContains(t, string(raw), "SECRET CONTENT")

	none, err := bs.RecentSenders(ctx, "beta", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, none, "outside the window")
}

func TestEntriesKeepACleanedLabelThatNeverAdmitsAndTheLogShowsWhatAnEditChanged(t *testing.T) {
	a, b := "x-"+strings.Repeat("a", 16), "x-"+strings.Repeat("b", 16)
	out, _, field, _ := AllowedProblem([]AllowedSender{{Server: "local", CrossTeamID: a, Label: "ops\x1b[31m team"}}, "")
	require.Empty(t, field)
	assert.Equal(t, "ops team", out[0].Label, "cleaned")
	set := BoardSettings{CrossTeamAllowed: out}
	assert.True(t, set.AllowsSender("local", a))
	assert.False(t, set.AllowsSender("local", b), "the label admits nobody")

	ctx := context.Background()
	bs := newAllowStore(t)
	require.NoError(t, bs.AppendEditLog(ctx, "beta", "Operator", "cross_team_allowed", `[{"server":"local","cross_team_id":"`+a+`","label":"one"}]`,
		`[{"server":"local","cross_team_id":"`+b+`","label":"two"}]`))
	log, err := bs.ListLog(ctx, "beta")
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, []AllowedSender{{Server: "local", CrossTeamID: b, Label: "two"}}, log[0].Added)
	assert.Equal(t, []AllowedSender{{Server: "local", CrossTeamID: a, Label: "one"}}, log[0].Removed)
}
