package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6381: the live list says an agent is "starting" while its CLI has not emitted its first event: a signal (the absence of the first event), bounded by the not-started window, never
// a timer of the page's own, and never true for a terminal, a sleeping or an archived session.
func TestAgentStartingIsTheAbsenceOfTheFirstEvent(t *testing.T) {
	for name, c := range map[string]struct {
		agentType, latest string
		age               float64
		sleeping, arch    bool
		want              bool
	}{
		"a new agent, no event yet":    {"claude", "", 2, false, false, true},
		"a Codex agent, no event yet":  {"codex", "", 40, false, false, true},
		"its first event arrived":      {"claude", "SessionStart", 2, false, false, false},
		"any event":                    {"claude", "Stop", 90, false, false, false},
		"a terminal is a shell":        {"terminal", "", 2, false, false, false},
		"asleep":                       {"claude", "", 2, true, false, false},
		"archived":                     {"claude", "", 2, false, true, false},
		"no creation time":             {"claude", "", 0, false, false, false},
		"past the window: not forever": {"claude", "", notStartedWindowSeconds + 1, false, false, false},
		"at the window's end":          {"claude", "", notStartedWindowSeconds, false, false, true},
	} {
		assert.Equal(t, c.want, agentStarting(c.agentType, c.latest, c.age, c.sleeping, c.arch), name)
	}
}

func TestTheLiveListCarriesStartingForANewAgent(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for id, mod := range map[string]func(*store.LiveSession){
		"00000000-0000-0000-0000-0000000000a1": func(l *store.LiveSession) {},
		"00000000-0000-0000-0000-0000000000a2": func(l *store.LiveSession) { l.CreatedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) },
		"00000000-0000-0000-0000-0000000000a3": func(l *store.LiveSession) { l.IsSleeping = 1 },
	} {
		terminal.addSession("claude-"+id, "/tmp/test")
		l := &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "agent", WorkingDir: "/tmp/test", CreatedAt: now}
		mod(l)
		require.NoError(t, ss.RegisterLiveSession(context.Background(), l))
	}
	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	got := map[string]any{}
	for _, r := range rows {
		got[r["session_id"].(string)] = r["starting"]
	}
	assert.Equal(t, true, got["00000000-0000-0000-0000-0000000000a1"], "a new agent with no event is starting")
	assert.Equal(t, false, got["00000000-0000-0000-0000-0000000000a2"], "one launched an hour ago is not")
	assert.Equal(t, false, got["00000000-0000-0000-0000-0000000000a3"], "a sleeping one is not")
}

// #6745 (Codex Test's #6742): a scheduled run's row kept starting=true after its CLI had reported (events of the kinds the latest-event read leaves out) and after the run finished.
func TestStartingClearsForAScheduledAgentThatReportedOrFinished(t *testing.T) {
	server, h, terminal, ss := setupSessionsTestServer(t)
	h.SetScheduleStore(store.NewScheduleStore(h.db))
	sched := store.NewScheduleStore(h.db)
	tasks := store.NewTaskStore(h.db)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	job, err := sched.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: t.TempDir(), Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	mk := func(id string) {
		terminal.addSession("claude-"+id, "/tmp/test")
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "job-agent", WorkingDir: "/tmp/test", CreatedAt: now, IsJob: 1}))
	}
	const fresh, reported, finished = "00000000-0000-0000-0000-0000000000b1", "00000000-0000-0000-0000-0000000000b2", "00000000-0000-0000-0000-0000000000b3"
	for _, id := range []string{fresh, reported, finished} {
		mk(id)
	}
	sid := reported
	_, err = tasks.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "job-agent", SessionID: &sid, EventType: "status", Summary: "working"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(ctx, job.ID, now, "completed")
	require.NoError(t, err)
	require.NoError(t, sched.UpdateScheduledRun(ctx, runID, map[string]interface{}{"session_id": finished}))
	runID2, err := sched.CreateScheduledRun(ctx, job.ID, now, "running")
	require.NoError(t, err)
	require.NoError(t, sched.UpdateScheduledRun(ctx, runID2, map[string]interface{}{"session_id": fresh}))

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	got := map[string]any{}
	for _, r := range rows {
		got[r["session_id"].(string)] = r["starting"]
	}
	assert.Equal(t, true, got[fresh], "a running scheduled agent with no event yet is starting")
	assert.Equal(t, false, got[reported], "its CLI has reported (a status event): no longer starting")
	assert.Equal(t, false, got[finished], "its run finished: not starting")
}
