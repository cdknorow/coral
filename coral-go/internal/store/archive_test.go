package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveByID(t *testing.T, ss *SessionStore, ctx context.Context) map[string]LiveSession {
	t.Helper()
	all, err := ss.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	out := map[string]LiveSession{}
	for _, ls := range all {
		out[ls.SessionID] = ls
	}
	return out
}

// Archived always implies sleeping: archiving sets both, unarchiving leaves the
// agent asleep, and any wake clears the archive.
func TestArchive_FlagsAndInvariant(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	registerTeam(t, ss, ctx, "b")

	require.NoError(t, ss.SetSessionArchived(ctx, "sess-lead", true)) // from awake
	got := liveByID(t, ss, ctx)["sess-lead"]
	assert.Equal(t, 1, got.IsSleeping)
	assert.Equal(t, 1, got.IsArchived)
	archived, err := ss.IsSessionArchived(ctx, "sess-lead")
	require.NoError(t, err)
	assert.True(t, archived)
	ls, err := ss.GetLiveSession(ctx, "sess-lead")
	require.NoError(t, err)
	assert.Equal(t, 1, ls.IsArchived, "GetLiveSession reads the flag")
	board, err := ss.GetBoardSessions(ctx, "b")
	require.NoError(t, err)
	for _, s := range board {
		assert.Equal(t, s.SessionID == "sess-lead", s.IsArchived == 1, s.SessionID)
	}

	require.NoError(t, ss.SetSessionArchived(ctx, "sess-lead", false))
	got = liveByID(t, ss, ctx)["sess-lead"]
	assert.Equal(t, 1, got.IsSleeping, "unarchive leaves it asleep")
	assert.Equal(t, 0, got.IsArchived)

	require.NoError(t, ss.SetSessionArchived(ctx, "sess-lead", true))
	require.NoError(t, ss.SetSessionSleeping(ctx, "sess-lead", true))
	assert.Equal(t, 1, liveByID(t, ss, ctx)["sess-lead"].IsArchived, "sleeping again keeps the archive")
	require.NoError(t, ss.SetSessionSleeping(ctx, "sess-lead", false))
	got = liveByID(t, ss, ctx)["sess-lead"]
	assert.Equal(t, 0, got.IsSleeping)
	assert.Equal(t, 0, got.IsArchived, "waking clears the archive")

	require.NoError(t, ss.SetSessionArchived(ctx, "sess-fe", true))
	_, err = ss.SetBoardSleeping(ctx, "b", false)
	require.NoError(t, err)
	assert.Equal(t, 0, liveByID(t, ss, ctx)["sess-fe"].IsArchived, "a board-wide wake clears it too")

	archived, err = ss.IsSessionArchived(ctx, "no-such-session")
	require.NoError(t, err)
	assert.False(t, archived)
	n, err := ss.CountActiveLiveSessions(ctx)
	require.NoError(t, err)
	require.NoError(t, ss.SetSessionArchived(ctx, "sess-qa", true))
	n2, err := ss.CountActiveLiveSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, n-1, n2, "an archived agent does not count against the live-agent limit")
}

// Orphan cleanup removes a sleeping duplicate of an awake agent, but not an
// archived one: the operator put that copy away on purpose.
func TestArchive_CleanupOrphanedSleepingKeepsArchived(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	dn := "Lead Developer"
	for _, id := range []string{"awake", "old-sleeping", "old-archived"} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &LiveSession{
			SessionID: id, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/repo", DisplayName: &dn, BoardName: strPtr("b"),
		}))
	}
	require.NoError(t, ss.SetSessionSleeping(ctx, "old-sleeping", true))
	require.NoError(t, ss.SetSessionArchived(ctx, "old-archived", true))

	cleaned, err := ss.CleanupOrphanedSleeping(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, cleaned)
	live := liveByID(t, ss, ctx)
	assert.Contains(t, live, "awake")
	assert.Contains(t, live, "old-archived")
	assert.NotContains(t, live, "old-sleeping")
}

// Team wake marks sleeping members active, except members whose session is archived.
func TestArchive_WakeTeamMembersSkipsArchived(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ts := NewTeamStore(db)
	ctx := context.Background()
	registerTeam(t, ss, ctx, "b")
	team, err := ts.CreateTeam(ctx, &Team{Name: "b", ConfigJSON: "{}", WorkingDir: "/tmp/repo"})
	require.NoError(t, err)
	members := map[string]int64{}
	for _, sid := range []string{"sess-lead", "sess-fe", "sess-qa"} {
		m, err := ts.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: sid, AgentConfigJSON: "{}", SessionID: strPtr(sid), Status: "sleeping"})
		require.NoError(t, err)
		members[sid] = m.ID
	}
	_, err = ts.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "no-session", AgentConfigJSON: "{}", Status: "sleeping"})
	require.NoError(t, err)
	require.NoError(t, ss.SetSessionArchived(ctx, "sess-qa", true))

	require.NoError(t, ts.WakeTeamMembers(ctx, team.ID))
	var rows []TeamMember
	require.NoError(t, db.SelectContext(ctx, &rows, "SELECT agent_name, status FROM team_members WHERE team_id = ?", team.ID))
	status := map[string]string{}
	for _, m := range rows {
		status[m.AgentName] = m.Status
	}
	assert.Equal(t, map[string]string{"sess-lead": "active", "sess-fe": "active", "sess-qa": "sleeping", "no-session": "active"}, status)
}
