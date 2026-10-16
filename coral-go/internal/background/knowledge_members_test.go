package background

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// knowledgeTeamFixture registers agents on a board, with a transcript each, and a team roster.
type knowledgeTeamFixture struct {
	t    *testing.T
	ctx  context.Context
	ss   *store.SessionStore
	ts   *store.TeamStore
	team *store.Team
	dir  string
}

func newKnowledgeTeamFixture(t *testing.T, board string) *knowledgeTeamFixture {
	db := setupTestDB(t)
	f := &knowledgeTeamFixture{t: t, ctx: context.Background(), ss: store.NewSessionStore(db), ts: store.NewTeamStore(db), dir: t.TempDir()}
	team, err := f.ts.CreateTeam(f.ctx, &store.Team{Name: board, Status: "running", WorkingDir: f.dir})
	require.NoError(t, err)
	f.team = team
	return f
}

// launch registers an agent on the board; withMember also gives it a roster row (launch-team).
func (f *knowledgeTeamFixture) launch(sessionID, name string, withMember bool) {
	board := f.team.Name
	require.NoError(f.t, f.ss.RegisterLiveSession(f.ctx, &store.LiveSession{SessionID: sessionID, AgentType: "claude",
		AgentName: name, WorkingDir: f.dir, DisplayName: &name, BoardName: &board}))
	f.transcript(sessionID, "work of "+name)
	if withMember {
		_, err := f.ts.CreateTeamMember(f.ctx, &store.TeamMember{TeamID: f.team.ID, AgentName: name,
			AgentConfigJSON: `{"name":"` + name + `"}`, SessionID: &sessionID, Status: "active"})
		require.NoError(f.t, err)
	}
}

func (f *knowledgeTeamFixture) transcript(sessionID, text string) {
	path := filepath.Join(f.dir, sessionID+".jsonl")
	require.NoError(f.t, os.WriteFile(path, []byte(`{"type":"assistant","message":{"content":"`+text+`"}}`+"\n"), 0o600))
	require.NoError(f.t, f.ss.UpsertSessionIndex(f.ctx, &store.SessionIndex{SessionID: sessionID, SourceType: "claude", SourceFile: path}))
}

// kill does what the kill route does: the member is stopped, then the session ends.
func (f *knowledgeTeamFixture) kill(sessionID string) {
	m, err := f.ts.GetMemberBySessionID(f.ctx, sessionID)
	require.NoError(f.t, err)
	require.NotNil(f.t, m, "the member is found by its current session id")
	require.NoError(f.t, f.ts.UpdateMemberStatus(f.ctx, m.ID, "stopped", m.SessionID))
	require.NoError(f.t, f.ss.UnregisterLiveSession(f.ctx, sessionID))
}

func (f *knowledgeTeamFixture) distilled() map[string]string {
	agents, err := teamKnowledgeAgents(f.ctx, f.ss, f.ts, f.team.Name)
	require.NoError(f.t, err)
	out := map[string]string{}
	for _, a := range agents {
		out[a.name] = a.transcript
	}
	return out
}

func sortedNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// #4074: a team of 2 that gains a 3rd agent on its board, has one put to sleep and one restarted,
// distills all 3, each with its transcript; a member killed on its own is left out.
func TestDistillCoversEveryCurrentTeamMember(t *testing.T) {
	f := newKnowledgeTeamFixture(t, "team-4074")
	f.launch("s-lead", "Lead", true)
	f.launch("s-qa", "QA", true)
	f.launch("s-ux", "UX", false)     // launched onto the board later: no roster row
	f.launch("s-gone", "Gone", true) // killed below: has left the team

	require.NoError(t, f.ss.SetSessionSleepingFor(f.ctx, "s-qa", ""))
	// A restart: the new session has no transcript of its own yet.
	resumeFrom := "s-lead"
	require.NoError(t, f.ss.ReplaceLiveSession(f.ctx, "s-lead", &store.LiveSession{SessionID: "s-lead-2",
		AgentType: "claude", AgentName: "Lead", WorkingDir: f.dir, ResumeFromID: &resumeFrom}))
	f.kill("s-gone")

	got := f.distilled()
	assert.Equal(t, []string{"Lead", "QA", "UX"}, sortedNames(got))
	assert.Contains(t, got["Lead"], "work of Lead", "a restarted agent's transcript comes from its resume chain")
	assert.Contains(t, got["QA"], "work of QA", "a sleeping agent is distilled")
	assert.Contains(t, got["UX"], "work of UX", "an agent launched onto the board later is distilled")

	// The roster followed the restart, so kill finds the member.
	m, err := f.ts.GetMemberBySessionID(f.ctx, "s-lead-2")
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "Lead", m.AgentName)

	// Once the restarted session has its own transcript, that one is used.
	f.transcript("s-lead-2", "newer work of Lead")
	assert.Contains(t, f.distilled()["Lead"], "newer work of Lead")
}

// A stopped team is still distilled from the members that stopped with it, not the one that
// left earlier.
func TestDistillStoppedTeamUsesMembersThatStoppedWithIt(t *testing.T) {
	f := newKnowledgeTeamFixture(t, "stopped-4074")
	f.launch("s-a", "A", true)
	f.launch("s-b", "B", true)
	f.launch("s-c", "C", true)
	f.kill("s-c")
	require.NoError(t, f.ts.StopTeam(f.ctx, f.team.ID))
	require.NoError(t, f.ss.UnregisterLiveSession(f.ctx, "s-a"))
	require.NoError(t, f.ss.UnregisterLiveSession(f.ctx, "s-b"))

	got := f.distilled()
	assert.Equal(t, []string{"A", "B"}, sortedNames(got))
	assert.Contains(t, got["A"], "work of A")
}

func TestDistillAgentsWithOneNameGetTheirOwnDocuments(t *testing.T) {
	f := newKnowledgeTeamFixture(t, "dup-4074")
	f.launch("s-1", "Dev", true)
	f.launch("s-2", "Dev", false)
	assert.Equal(t, []string{"Dev", "Dev (2)"}, sortedNames(f.distilled()))
}

func TestDistillUnknownTeamFails(t *testing.T) {
	f := newKnowledgeTeamFixture(t, "empty-4074")
	_, err := teamKnowledgeAgents(f.ctx, f.ss, f.ts, "no-such-team")
	assert.ErrorContains(t, err, "not found")
	_, err = teamKnowledgeAgents(f.ctx, f.ss, f.ts, f.team.Name)
	assert.ErrorContains(t, err, "not found")
}
