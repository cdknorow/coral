package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// archiveTestServer serves the sleep/wake/archive routes of a SessionsHandler
// backed by the mock terminal, with a team store.
func archiveTestServer(t *testing.T) (*httptest.Server, *SessionsHandler, *mockSessionTerminal, *store.SessionStore, *store.TeamStore) {
	t.Helper()
	_, handler, terminal, ss := setupSessionsTestServer(t)
	ts := store.NewTeamStore(handler.db)
	handler.SetTeamStore(ts)
	r := chi.NewRouter()
	r.Post("/api/sessions/live/wake-all", handler.WakeAll)
	r.Post("/api/sessions/live/team/{boardName}/wake", handler.Wake)
	r.Post("/api/sessions/live/{sessionID}/wake", handler.WakeSession)
	r.Post("/api/sessions/live/{sessionID}/archive", handler.ArchiveSession)
	r.Post("/api/sessions/live/{sessionID}/unarchive", handler.UnarchiveSession)
	r.Get("/api/board/{project}/status", handler.TeamAvailability)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, handler, terminal, ss, ts
}

// registerTerminalAgent registers a live "terminal" session (waking it starts
// no agent CLI) on board, running in the mock terminal unless sleeping.
func registerTerminalAgent(t *testing.T, ss *store.SessionStore, terminal *mockSessionTerminal, sid, board string, sleeping bool) {
	t.Helper()
	name := "Agent " + sid[:4]
	// The mock terminal's KillSessionOnly removes the session named by the agent
	// name, so the agent name is the session name here.
	ls := &store.LiveSession{SessionID: sid, AgentType: "terminal", AgentName: naming.SessionName("terminal", sid), WorkingDir: t.TempDir(), DisplayName: &name, BoardName: &board}
	if sleeping {
		ls.IsSleeping = 1
	}
	require.NoError(t, ss.RegisterLiveSession(context.Background(), ls))
	if !sleeping {
		require.NoError(t, terminal.CreateSession(context.Background(), naming.SessionName("terminal", sid), ls.WorkingDir))
	}
}

func postArchiveOK(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func archFlags(t *testing.T, ss *store.SessionStore, sid string) (sleeping, archived int) {
	t.Helper()
	ls, err := ss.GetLiveSession(context.Background(), sid)
	require.NoError(t, err)
	require.NotNil(t, ls)
	return ls.IsSleeping, ls.IsArchived
}

const (
	archSID1 = "11111111-1111-4111-8111-111111111111"
	archSID2 = "22222222-2222-4222-8222-222222222222"
	archSID3 = "33333333-3333-4333-8333-333333333333"
)

// Archive puts a running agent to sleep (its terminal is killed) and marks it
// archived; unarchive returns it to plain sleeping; waking an archived agent
// unarchives it.
func TestArchiveSession_ArchiveUnarchiveWake(t *testing.T) {
	srv, _, terminal, ss, _ := archiveTestServer(t)
	registerTerminalAgent(t, ss, terminal, archSID1, "b", false)
	base := srv.URL + "/api/sessions/live/" + archSID1

	assert.Equal(t, map[string]any{"ok": true, "sleeping": true, "archived": true}, postArchiveOK(t, base+"/archive"))
	s, a := archFlags(t, ss, archSID1)
	assert.Equal(t, [2]int{1, 1}, [2]int{s, a})
	assert.False(t, terminal.HasSession(context.Background(), naming.SessionName("terminal", archSID1)), "archiving a running agent stops it")

	assert.Equal(t, true, postArchiveOK(t, base+"/archive")["ok"], "archiving again is harmless")

	assert.Equal(t, map[string]any{"ok": true, "sleeping": true, "archived": false}, postArchiveOK(t, base+"/unarchive"))
	s, a = archFlags(t, ss, archSID1)
	assert.Equal(t, [2]int{1, 0}, [2]int{s, a}, "unarchive leaves it asleep")

	postArchiveOK(t, base+"/archive")
	assert.Equal(t, true, postArchiveOK(t, base+"/wake")["ok"])
	s, a = archFlags(t, ss, archSID1)
	assert.Equal(t, [2]int{0, 0}, [2]int{s, a}, "an explicit wake unarchives")

	assert.Equal(t, false, postArchiveOK(t, srv.URL+"/api/sessions/live/"+archSID3+"/archive")["ok"], "unknown session")
}

// Wake All and team wake leave archived agents asleep, and team wake keeps an
// archived member's team status at sleeping.
func TestArchiveSession_BulkWakesSkipArchived(t *testing.T) {
	srv, _, terminal, ss, ts := archiveTestServer(t)
	ctx := context.Background()
	registerTerminalAgent(t, ss, terminal, archSID1, "b", true)
	registerTerminalAgent(t, ss, terminal, archSID2, "b", true)
	registerTerminalAgent(t, ss, terminal, archSID3, "c", true)
	require.NoError(t, ss.SetSessionArchived(ctx, archSID2, true))

	out := postArchiveOK(t, srv.URL+"/api/sessions/live/wake-all")
	assert.Equal(t, float64(2), out["sessions_relaunched"])
	for sid, want := range map[string][2]int{archSID1: {0, 0}, archSID2: {1, 1}, archSID3: {0, 0}} {
		s, a := archFlags(t, ss, sid)
		assert.Equal(t, want, [2]int{s, a}, sid)
	}

	// Team wake: board b asleep again, with a sleeping team.
	require.NoError(t, ss.SetSessionSleeping(ctx, archSID1, true))
	team, err := ts.CreateTeam(ctx, &store.Team{Name: "b", ConfigJSON: "{}", WorkingDir: "/tmp"})
	require.NoError(t, err)
	require.NoError(t, ts.UpdateTeamStatus(ctx, team.ID, "sleeping"))
	for _, sid := range []string{archSID1, archSID2} {
		sid := sid
		_, err := ts.CreateTeamMember(ctx, &store.TeamMember{TeamID: team.ID, AgentName: sid, AgentConfigJSON: "{}", SessionID: &sid, Status: "sleeping"})
		require.NoError(t, err)
	}
	out = postArchiveOK(t, srv.URL+"/api/sessions/live/team/b/wake")
	assert.Equal(t, float64(1), out["sessions_relaunched"])
	s, a := archFlags(t, ss, archSID1)
	assert.Equal(t, [2]int{0, 0}, [2]int{s, a})
	s, a = archFlags(t, ss, archSID2)
	assert.Equal(t, [2]int{1, 1}, [2]int{s, a}, "team wake leaves the archived member asleep")
	var members []store.TeamMember
	require.NoError(t, ss.DB().SelectContext(ctx, &members, "SELECT agent_name, status FROM team_members WHERE team_id = ?", team.ID))
	status := map[string]string{}
	for _, m := range members {
		status[m.AgentName] = m.Status
	}
	assert.Equal(t, map[string]string{archSID1: "active", archSID2: "sleeping"}, status)
}

// coral-board status never offers an archived agent for work.
func TestArchiveSession_TeamAvailabilityReportsArchived(t *testing.T) {
	srv, _, terminal, ss, _ := archiveTestServer(t)
	registerTerminalAgent(t, ss, terminal, archSID1, "b", true)
	registerTerminalAgent(t, ss, terminal, archSID2, "b", true)
	require.NoError(t, ss.SetSessionArchived(context.Background(), archSID2, true))

	resp, err := http.Get(srv.URL + "/api/board/b/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Agents  []availableAgent `json:"agents"`
		Summary map[string]int   `json:"summary"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	got := map[string]availableAgent{}
	for _, a := range body.Agents {
		got[a.SessionID] = a
	}
	require.Contains(t, got, archSID2)
	assert.Equal(t, "archived", got[archSID2].Availability)
	assert.False(t, got[archSID2].Available)
	assert.Contains(t, got[archSID2].Reason, "unarchive")
	assert.Equal(t, "sleeping", got[archSID1].Availability)
	assert.Equal(t, 1, body.Summary["archived"])
	assert.Equal(t, 1, body.Summary["sleeping"])
}

// Tasks are never assigned to an archived agent: create, edit and reassign
// refuse it with 409 assignee_archived (telling the caller to unarchive first),
// and task nudges skip it.
func TestBoard_RefusesArchivedAssignee(t *testing.T) {
	srv, handler := setupBoardTestServer(t)
	ctx := context.Background()
	archivedSession := "claude-" + archSID1
	handler.SetArchivedSessionLookup(func(_ context.Context, sessionName string) bool { return sessionName == archivedSession })
	terminal := newMockTerminal()
	handler.SetTerminal(terminal)
	require.NoError(t, terminal.CreateSession(ctx, archivedSession, "/tmp"))
	require.NoError(t, terminal.CreateSession(ctx, "claude-"+archSID2, "/tmp"))
	for sub, sess := range map[string]string{"Archie": archivedSession, "Wally": "claude-" + archSID2} {
		_, err := handler.bs.Subscribe(ctx, "p", sub, "Developer", sess, nil, nil, "")
		require.NoError(t, err)
	}

	do := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	refused := func(code int, out map[string]any, what string) {
		t.Helper()
		assert.Equal(t, http.StatusConflict, code, what)
		assert.Equal(t, "assignee_archived", out["code"], what)
		assert.Contains(t, out["error"], "unarchive", what)
	}

	code, out := do("POST", "/api/board/p/tasks", map[string]any{"title": "t1", "subscriber_id": "Operator", "assigned_to": "Archie"})
	refused(code, out, "create")
	code, out = do("POST", "/api/board/p/tasks", map[string]any{"title": "t2", "subscriber_id": "Operator", "assigned_to": "Wally"})
	require.Equal(t, http.StatusCreated, code, out)
	id := int64(out["id"].(float64))
	code, _ = do("POST", "/api/board/p/tasks", map[string]any{"title": "t3", "subscriber_id": "Operator"})
	assert.Equal(t, http.StatusCreated, code, "unassigned tasks are unaffected")

	code, out = do("POST", fmt.Sprintf("/api/board/p/tasks/%d/reassign", id), map[string]any{"subscriber_id": "Operator", "assignee": "Archie"})
	refused(code, out, "reassign")
	code, out = do("PATCH", fmt.Sprintf("/api/board/p/tasks/%d", id), map[string]any{"subscriber_id": "Operator", "assigned_to": "Archie"})
	refused(code, out, "edit")
	task, err := handler.bs.GetTask(ctx, "p", id)
	require.NoError(t, err)
	require.NotNil(t, task.AssignedTo)
	assert.Equal(t, "Wally", *task.AssignedTo, "the refused changes left the task with Wally")

	assert.Error(t, handler.sendTaskNudge(ctx, "p", "Archie", taskNudge))
	assert.NoError(t, handler.sendTaskNudge(ctx, "p", "Wally", taskNudge))

	// An unassigned task nudges one idle subscriber, picked at random; it is never
	// the archived one. Before the fix Archie was picked about half the time.
	for i := range 20 {
		code, _ := do("POST", "/api/board/p/tasks", map[string]any{"title": fmt.Sprintf("pool %d", i), "subscriber_id": "Operator"})
		require.Equal(t, http.StatusCreated, code)
	}
	countNudges := func(session string) int {
		terminal.mu.Lock()
		defer terminal.mu.Unlock()
		n := 0
		for _, s := range terminal.sent[session] {
			if s == taskNudge {
				n++
			}
		}
		return n
	}
	// 1 direct nudge + t3 + 20 pool tasks, all to Wally.
	require.Eventually(t, func() bool { return countNudges("claude-"+archSID2) == 22 }, 5*time.Second, 10*time.Millisecond)
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.sent[archivedSession], "an archived agent is never nudged")
}

// The store-backed lookup the server wires into the board.
func TestArchivedSessionLookup(t *testing.T) {
	_, _, terminal, ss, _ := archiveTestServer(t)
	registerTerminalAgent(t, ss, terminal, archSID1, "b", true)
	registerTerminalAgent(t, ss, terminal, archSID2, "b", true)
	require.NoError(t, ss.SetSessionArchived(context.Background(), archSID1, true))
	lookup := ArchivedSessionLookup(ss)
	ctx := context.Background()
	assert.True(t, lookup(ctx, "claude-"+archSID1))
	assert.False(t, lookup(ctx, "claude-"+archSID2))
	assert.False(t, lookup(ctx, "not-a-session"))
	assert.False(t, lookup(ctx, "claude-"+archSID3), "unknown session")
}
