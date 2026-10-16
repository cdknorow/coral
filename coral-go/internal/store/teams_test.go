package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTeamStore_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{
		Name:       "my-team",
		ConfigJSON: `{"agents":[]}`,
		WorkingDir: "/tmp/repo",
		IsWorktree: 0,
	})
	require.NoError(t, err)
	assert.True(t, team.ID > 0)
	assert.Equal(t, "running", team.Status)
	assert.NotEmpty(t, team.CreatedAt)

	got, err := s.GetTeam(ctx, "my-team")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, team.ID, got.ID)
	assert.Equal(t, "my-team", got.Name)
	assert.Equal(t, "running", got.Status)
	assert.Equal(t, "/tmp/repo", got.WorkingDir)
	assert.NotNil(t, got.Config)
	assert.Empty(t, got.Members)
}

func TestTeamStore_GetTeam_NotFound(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	got, err := s.GetTeam(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTeamStore_ListTeams(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	_, err := s.CreateTeam(ctx, &Team{Name: "team-a", ConfigJSON: "{}", WorkingDir: "/a"})
	require.NoError(t, err)
	_, err = s.CreateTeam(ctx, &Team{Name: "team-b", ConfigJSON: "{}", WorkingDir: "/b"})
	require.NoError(t, err)

	// List all
	teams, err := s.ListTeams(ctx, "")
	require.NoError(t, err)
	assert.Len(t, teams, 2)

	// List by status
	teams, err = s.ListTeams(ctx, "running")
	require.NoError(t, err)
	assert.Len(t, teams, 2)

	teams, err = s.ListTeams(ctx, "stopped")
	require.NoError(t, err)
	assert.Len(t, teams, 0)
}

func TestTeamStore_UpdateTeamStatus(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "status-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	// Stop team
	err = s.UpdateTeamStatus(ctx, team.ID, "stopped")
	require.NoError(t, err)

	got, err := s.GetTeam(ctx, "status-team")
	require.NoError(t, err)
	assert.Equal(t, "stopped", got.Status)
	assert.NotNil(t, got.StoppedAt)

	// Restart clears stopped_at
	err = s.UpdateTeamStatus(ctx, team.ID, "running")
	require.NoError(t, err)

	got, err = s.GetTeam(ctx, "status-team")
	require.NoError(t, err)
	assert.Equal(t, "running", got.Status)
	assert.Nil(t, got.StoppedAt)
}

func TestTeamStore_DeleteTeam(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "del-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	// Can't delete running team
	err = s.DeleteTeam(ctx, team.ID)
	assert.Error(t, err)

	// Stop, then delete
	require.NoError(t, s.UpdateTeamStatus(ctx, team.ID, "stopped"))
	err = s.DeleteTeam(ctx, team.ID)
	require.NoError(t, err)

	got, err := s.GetTeam(ctx, "del-team")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTeamStore_Members(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "member-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	sid := "claude-abc"
	m1, err := s.CreateTeamMember(ctx, &TeamMember{
		TeamID:          team.ID,
		AgentName:       "Lead Developer",
		AgentConfigJSON: `{"prompt":"build it"}`,
		SessionID:       &sid,
	})
	require.NoError(t, err)
	assert.True(t, m1.ID > 0)
	assert.Equal(t, "active", m1.Status)

	sid2 := "claude-def"
	m2, err := s.CreateTeamMember(ctx, &TeamMember{
		TeamID:          team.ID,
		AgentName:       "QA Engineer",
		AgentConfigJSON: `{"prompt":"test it"}`,
		SessionID:       &sid2,
	})
	require.NoError(t, err)

	// Get active members
	active, err := s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 2)

	// Stop one member
	err = s.UpdateMemberStatus(ctx, m2.ID, "stopped", &sid2)
	require.NoError(t, err)

	active, err = s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 1)
	assert.Equal(t, "Lead Developer", active[0].AgentName)

	// Get team with members
	got, err := s.GetTeam(ctx, "member-team")
	require.NoError(t, err)
	assert.Len(t, got.Members, 2)

	// Lookup by session ID
	found, err := s.GetMemberBySessionID(ctx, "claude-abc")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "Lead Developer", found.AgentName)
	assert.Equal(t, m1.ID, found.ID)
}

func TestTeamStore_SleepWakeCycle(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "sleep-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	sid1 := "claude-1"
	sid2 := "claude-2"
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent1", AgentConfigJSON: "{}", SessionID: &sid1})
	require.NoError(t, err)
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent2", AgentConfigJSON: "{}", SessionID: &sid2})
	require.NoError(t, err)

	// Sleep: active → sleeping
	err = s.SetAllMembersStatus(ctx, team.ID, "active", "sleeping")
	require.NoError(t, err)
	err = s.UpdateTeamStatus(ctx, team.ID, "sleeping")
	require.NoError(t, err)

	sleeping, err := s.GetSleepingMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, sleeping, 2)

	active, err := s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 0)

	// Wake: sleeping → active
	err = s.SetAllMembersStatus(ctx, team.ID, "sleeping", "active")
	require.NoError(t, err)
	err = s.UpdateTeamStatus(ctx, team.ID, "running")
	require.NoError(t, err)

	active, err = s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 2)
}

func TestTeamStore_Resurrect(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "resurrect-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	sid1 := "claude-1"
	sid2 := "claude-2"
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent1", AgentConfigJSON: "{}", SessionID: &sid1})
	require.NoError(t, err)
	m2, err := s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent2", AgentConfigJSON: "{}", SessionID: &sid2})
	require.NoError(t, err)

	// Kill Agent2 individually first
	err = s.UpdateMemberStatus(ctx, m2.ID, "stopped", &sid2)
	require.NoError(t, err)

	// Kill team — only Agent1 transitions (uses atomic StopTeam for timestamp match)
	err = s.StopTeam(ctx, team.ID)
	require.NoError(t, err)

	// Resurrect should only return Agent1 (stopped_at matches team's stopped_at)
	members, err := s.GetMembersForResurrect(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)
	assert.Equal(t, "Agent1", members[0].AgentName)
}

func TestTeamStore_CascadeDelete(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "cascade-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent1", AgentConfigJSON: "{}"})
	require.NoError(t, err)

	// Stop and delete
	require.NoError(t, s.UpdateTeamStatus(ctx, team.ID, "stopped"))
	require.NoError(t, s.DeleteTeam(ctx, team.ID))

	// Members should be gone too (CASCADE)
	active, err := s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 0)
}

func TestTeamStore_RestartAgent_UpdatesSessionID(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "restart-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	oldSID := "claude-old"
	m, err := s.CreateTeamMember(ctx, &TeamMember{
		TeamID: team.ID, AgentName: "Agent1", AgentConfigJSON: "{}", SessionID: &oldSID,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", m.Status)

	// Simulate restart: update session_id but keep active status
	newSID := "claude-new"
	err = s.UpdateMemberStatus(ctx, m.ID, "active", &newSID)
	require.NoError(t, err)

	// Verify session_id changed, status remains active
	found, err := s.GetMemberBySessionID(ctx, "claude-new")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "active", found.Status)
	assert.Equal(t, m.ID, found.ID)

	// Old session_id no longer resolves
	old, err := s.GetMemberBySessionID(ctx, "claude-old")
	require.NoError(t, err)
	assert.Nil(t, old)
}

func TestTeamStore_SleepWake_StoppedMemberStaysStopped(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "mixed-team", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)

	sid1 := "claude-1"
	sid2 := "claude-2"
	sid3 := "claude-3"
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Active1", AgentConfigJSON: "{}", SessionID: &sid1})
	require.NoError(t, err)
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Active2", AgentConfigJSON: "{}", SessionID: &sid2})
	require.NoError(t, err)
	m3, err := s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Killed", AgentConfigJSON: "{}", SessionID: &sid3})
	require.NoError(t, err)

	// Kill one member individually
	err = s.UpdateMemberStatus(ctx, m3.ID, "stopped", &sid3)
	require.NoError(t, err)

	// Sleep the team — only active members should transition to sleeping
	err = s.SetAllMembersStatus(ctx, team.ID, "active", "sleeping")
	require.NoError(t, err)
	err = s.UpdateTeamStatus(ctx, team.ID, "sleeping")
	require.NoError(t, err)

	sleeping, err := s.GetSleepingMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, sleeping, 2, "only the 2 active members should be sleeping")

	// Verify the individually stopped member is still stopped, not sleeping
	got, err := s.GetTeam(ctx, "mixed-team")
	require.NoError(t, err)
	var stoppedCount int
	for _, m := range got.Members {
		if m.Status == "stopped" {
			stoppedCount++
			assert.Equal(t, "Killed", m.AgentName)
		}
	}
	assert.Equal(t, 1, stoppedCount, "individually killed member should remain stopped")

	// Wake the team — sleeping members become active again
	err = s.SetAllMembersStatus(ctx, team.ID, "sleeping", "active")
	require.NoError(t, err)
	err = s.UpdateTeamStatus(ctx, team.ID, "running")
	require.NoError(t, err)

	active, err := s.GetActiveMembers(ctx, team.ID)
	require.NoError(t, err)
	assert.Len(t, active, 2, "only previously sleeping members should wake")

	// The stopped member should still be stopped
	got, err = s.GetTeam(ctx, "mixed-team")
	require.NoError(t, err)
	stoppedCount = 0
	for _, m := range got.Members {
		if m.Status == "stopped" {
			stoppedCount++
		}
	}
	assert.Equal(t, 1, stoppedCount, "individually killed member should still be stopped after wake")
}

func TestTeamStore_GetTeamByID(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, &Team{Name: "id-team", ConfigJSON: `{"key":"val"}`, WorkingDir: "/x"})
	require.NoError(t, err)

	got, err := s.GetTeamByID(ctx, team.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "id-team", got.Name)
	assert.NotNil(t, got.Config)

	// Non-existent ID
	got, err = s.GetTeamByID(ctx, 99999)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTeamStore_HydrateConfig(t *testing.T) {
	team := &Team{ConfigJSON: `{"agents":[{"name":"dev"}]}`}
	team.HydrateConfig()
	assert.NotNil(t, team.Config)
	assert.Contains(t, string(team.Config), "dev")

	// Empty config
	team2 := &Team{ConfigJSON: ""}
	team2.HydrateConfig()
	assert.Nil(t, team2.Config)

	member := &TeamMember{AgentConfigJSON: `{"prompt":"test"}`}
	member.HydrateConfig()
	assert.NotNil(t, member.AgentConfig)
	assert.Contains(t, string(member.AgentConfig), "test")
}

// #3920: a member stopped on its own at (or after) the instant the team is then stopped is not
// resurrected with the team: the team stop takes an instant after every earlier member stop.
func TestTeamStore_ResurrectSkipsAMemberStoppedOnTheSameInstant(t *testing.T) {
	db := openTestDB(t)
	s := NewTeamStore(db)
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, &Team{Name: "same-instant", ConfigJSON: "{}", WorkingDir: "/x"})
	require.NoError(t, err)
	sid1, sid2 := "claude-1", "claude-2"
	_, err = s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent1", AgentConfigJSON: "{}", SessionID: &sid1})
	require.NoError(t, err)
	m2, err := s.CreateTeamMember(ctx, &TeamMember{TeamID: team.ID, AgentName: "Agent2", AgentConfigJSON: "{}", SessionID: &sid2})
	require.NoError(t, err)
	// Agent2 stopped on its own on the very instant the team is then stopped (one clock tick).
	const instant = "2026-10-16T12:00:00.000000+00:00"
	old := teamStopNow
	teamStopNow = func() string { return instant }
	defer func() { teamStopNow = old }()
	_, err = db.ExecContext(ctx, "UPDATE team_members SET status = 'stopped', stopped_at = ? WHERE id = ?", instant, m2.ID)
	require.NoError(t, err)
	require.NoError(t, s.StopTeam(ctx, team.ID))
	members, err := s.GetMembersForResurrect(ctx, team.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "Agent1", members[0].AgentName)
}
