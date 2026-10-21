package background

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
)

func sessionOf(n int) string {
	return naming.SessionName("claude", fmt.Sprintf("%08d-0000-0000-0000-000000000000", n))
}

// wait registers name's coral-board wait (kind: message, task, commit, any or operator).
func (h *checkHarness) wait(n int, name, kind, target string) {
	h.t.Helper()
	_, err := h.s.RegisterWait(context.Background(), "team", name, sessionOf(n), kind, target, "", 3*time.Hour)
	require.NoError(h.t, err)
}

// sleeping marks the member numbered n asleep in a sessions database the store reads, and takes it out of the live agents.
func (h *checkHarness) sleeping(n int) {
	h.t.Helper()
	if h.sessions == nil {
		db, err := sqlx.Open("sqlite", filepath.Join(h.t.TempDir(), "sessions.db"))
		require.NoError(h.t, err)
		h.t.Cleanup(func() { db.Close() })
		_, err = db.Exec(`CREATE TABLE live_sessions (session_id TEXT PRIMARY KEY, is_sleeping INTEGER NOT NULL DEFAULT 0)`)
		require.NoError(h.t, err)
		h.s.SetSessionsDB(db)
		h.sessions = db
	}
	sid := fmt.Sprintf("%08d-0000-0000-0000-000000000000", n)
	_, err := h.sessions.Exec(`INSERT OR REPLACE INTO live_sessions (session_id, is_sleeping) VALUES (?, 1)`, sid)
	require.NoError(h.t, err)
	kept := h.agents[:0]
	for _, a := range h.agents {
		if a.SessionID != sid {
			kept = append(kept, a)
		}
	}
	h.agents = kept
}

func (h *checkHarness) check() string {
	h.t.Helper()
	h.mu.Lock()
	n := len(h.sent["Orchestrator"])
	h.mu.Unlock()
	h.m.mu.Lock()
	delete(h.m.lastCheck, "team") // the interval is not what is tested
	h.m.mu.Unlock()
	h.m.scan(context.Background())
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Greater(h.t, len(h.sent["Orchestrator"]), n, "no check typed to the Orchestrator")
	got := h.sent["Orchestrator"]
	return got[len(got)-1]
}

// Every agent is classified from its real state, and "progressing" is never said from post volume (the incident: ten agents
// "progressing; 55 posts" while eight sat in coral-board wait).
func TestRosterClassifiesEveryAgent(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Worker", "Developer", 2, 0)             // in a turn: working
	h.member("Waiter", "Developer", 3, 0)             // in a wait: waiting
	h.member("Idler", "Developer", 4, 40*time.Minute) // awake, no turn, no wait: idle
	h.member("Prompted", "Developer", 5, 0)           // at a prompt
	h.sig[fmt.Sprintf("%08d-0000-0000-0000-000000000000", 5)] = agentSignal{NeedsInput: true, Since: h.now}
	h.member("Blind", "Developer", 6, 0) // no hook events: unknown
	delete(h.sig, fmt.Sprintf("%08d-0000-0000-0000-000000000000", 6))
	h.member("Sleeper", "Developer", 7, 0) // asleep
	h.sleeping(7)
	_, err := h.s.Subscribe(ctx, "team", "Gone", "Developer", sessionOf(8), nil, "all") // a session with nothing running: offline
	require.NoError(t, err)
	h.wait(3, "Waiter", "message", "Orchestrator")
	for i := 0; i < 55; i++ { // lots of posts: volume says nothing
		_, err := h.s.PostMessage(ctx, "team", "Worker", fmt.Sprintf("progress %d", i), nil)
		require.NoError(t, err)
	}
	out := h.check()
	assert.Contains(t, out, "[Coral health check] "+h.now.UTC().Format("15:04Z")+" · 1 working · 1 WAITING · 1 at a prompt · 1 idle · 1 of unknown state · 1 asleep · 1 offline · 0 claimable tasks")
	assert.NotContains(t, strings.ToLower(out), "progressing")
	assert.NotContains(t, out, "awake agents are working")
	assert.NotContains(t, out, "\n")
	assert.Contains(t, out, "Waiting on YOU (Orchestrator), 1: Waiter ")
	assert.Contains(t, out, "posts.", "volume is only counted")
}

// Waits are grouped by who they wait on, the recipient's first; a task wait is on the task's owner, a role names its member, an
// operator wait is left to the operator's questions, and a wait for a commit or for anything has its own group.
func TestRosterGroupsWaitsByWhoTheyWaitOn(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Security Reviewer", "Security", 2, 0)
	for n, name := range []string{"Dev A", "Dev B", "Dev C", "Dev D", "Dev E", "Dev F", "Dev G"} {
		h.member(name, "Developer", 10+n, 0)
	}
	owned, err := h.s.CreateTask(ctx, "team", "owned by security", "", "medium", "Operator", "Security Reviewer")
	require.NoError(t, err)
	h.wait(10, "Dev A", "message", "Orchestrator")      // on the recipient, by name
	h.wait(11, "Dev B", "message", "orchestrator")      // on the recipient, by role and case
	h.wait(12, "Dev C", "message", "Security Reviewer") // on Security, by name
	h.wait(13, "Dev D", "task", fmt.Sprint(owned.ID))   // on the task's owner: Security
	h.wait(14, "Dev E", "commit", "abc123")             // on a commit
	h.wait(15, "Dev F", "operator", "")                 // on the operator: not a group
	h.wait(16, "Dev G", "message", "Nobody Here")       // a name that is no member: its own group
	out := h.check()
	assert.Contains(t, out, "7 WAITING")
	iYou := strings.Index(out, "Waiting on YOU (Orchestrator), 2: ")
	iSec := strings.Index(out, "Waiting on Security Reviewer, 2: ")
	require.True(t, iYou >= 0 && iSec > iYou, "the recipient's group first, then the larger: %s", out)
	assert.Contains(t, out, "Dev A ")
	assert.Contains(t, out, "Dev B ")
	assert.Contains(t, out, "Waiting on a commit, 1: Dev E ")
	assert.Contains(t, out, "Waiting on Nobody Here, 1: Dev G ")
	assert.NotContains(t, out, "Waiting on the Operator", "an operator wait is the operator's question, not a group")
	assert.Equal(t, 1, strings.Count(out, "Dev F"), "named once, with the operator's questions")
}

// STALE: the awaited party has mentioned the waiter since the wait began, yet the waiter has not woken.
func TestRosterFlagsStaleWaits(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Security Reviewer", "Security", 2, 0)
	h.member("Lead", "Lead Developer", 3, 0)
	h.member("Front", "Frontend Dev", 4, 0)
	_, err := h.s.PostMessage(ctx, "team", "Security Reviewer", "@Lead earlier message, before the wait", nil)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // message times have a one second resolution
	h.wait(3, "Lead", "message", "Security Reviewer")
	h.wait(4, "Front", "message", "Security Reviewer")
	out := h.check()
	assert.NotContains(t, out, "STALE", "a message from before the wait, and none for Front: nothing is stale")
	time.Sleep(1100 * time.Millisecond)
	_, err = h.s.PostMessage(ctx, "team", "Security Reviewer", "@Lead please look at the patch", nil)
	require.NoError(t, err)
	_, err = h.s.PostMessage(ctx, "team", "Orchestrator", "@Front a message from someone else", nil) // not from who Front waits on
	require.NoError(t, err)
	h.now = time.Now().UTC().Add(time.Minute)
	out = h.check()
	assert.Contains(t, out, "Lead ")
	assert.Contains(t, out, "(STALE: Security Reviewer has posted a message mentioning them since the wait started)")
	assert.Equal(t, 1, strings.Count(out, "STALE"), "only Lead: Front was mentioned by the Orchestrator, not by who it waits on")
}

// A sleeping agent with messages tagged to it unread is named, with how many and since when; one that read them, or was never
// tagged, is not.
func TestRosterNamesSleepersWithUnreadTags(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Digital Ocean Expert", "Cloud", 2, 0)
	h.member("Quiet Sleeper", "Cloud", 3, 0)
	h.member("Reader", "Cloud", 4, 0)
	h.sleeping(2)
	h.sleeping(3)
	h.sleeping(4)
	_, err := h.s.PostMessage(ctx, "team", "Orchestrator", "@Digital Ocean Expert the droplet is yours", nil)
	require.NoError(t, err)
	_, err = h.s.PostMessage(ctx, "team", "Orchestrator", "@Digital Ocean Expert and the firewall too", nil)
	require.NoError(t, err)
	_, err = h.s.PostMessage(ctx, "team", "Orchestrator", "@Reader read this", nil)
	require.NoError(t, err)
	msgs, err := h.s.ReadMessages(ctx, "team", "Reader", 100) // Reader reads everything
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	out := h.check()
	assert.Contains(t, out, "3 asleep")
	assert.Contains(t, out, "Asleep with unread tagged messages: Digital Ocean Expert (2 tagged since ")
	assert.NotContains(t, out, "Quiet Sleeper (")
	assert.NotContains(t, out, "Reader (")
}

// Rule 6: someone who has waited more than ten minutes on X is told to X once, as a direct mention, per wait.
func TestRosterEscalatesALongWaitOncePerWait(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Security Reviewer", "Security", 2, 0)
	h.member("Lead Developer", "Developer", 3, 0)
	h.member("Operator Waiter", "Developer", 4, 0)
	h.wait(3, "Lead Developer", "message", "Security Reviewer")
	h.wait(4, "Operator Waiter", "operator", "")
	posted := func() []string {
		msgs, err := h.s.ListMessages(ctx, "team", 100, 0, 0)
		require.NoError(t, err)
		var out []string
		for _, m := range msgs {
			if m.SubscriberID == "Coral Health Monitor" && strings.Contains(m.Content, "has waited") {
				out = append(out, m.Content)
			}
		}
		return out
	}
	h.m.scan(ctx)
	assert.Empty(t, posted(), "five minutes is not ten")
	h.now = h.now.Add(20 * time.Minute) // the wait is now about twenty minutes old
	h.m.scan(ctx)
	got := posted()
	require.Len(t, got, 1, "the operator's wait is nobody's to be told")
	assert.True(t, strings.HasPrefix(got[0], "@Security Reviewer Lead Developer has waited "), got[0])
	assert.Contains(t, got[0], " on you")
	h.m.scan(ctx)
	h.now = h.now.Add(30 * time.Minute)
	h.m.scan(ctx)
	assert.Len(t, posted(), 1, "once per wait, however many scans")
	// the same agent waits again, on the same party: a new wait, a new escalation
	h.wait(3, "Lead Developer", "message", "Security Reviewer")
	h.now = time.Now().UTC().Add(time.Minute)
	h.m.scan(ctx)
	assert.Len(t, posted(), 1, "the new wait is young")
	h.now = h.now.Add(25 * time.Minute)
	h.m.scan(ctx)
	assert.Len(t, posted(), 2)
}

// A check is sent when the roster has something for the reader, even with no item and with the all-clear line off.
func TestRosterWaitsAreNotSilenced(t *testing.T) {
	h := newCheckHarness(t)
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Dev", "Developer", 2, 0)
	h.wait(2, "Dev", "message", "Orchestrator")
	hs := healthOn()
	hs.AllClear = false
	_, err := h.s.UpdateBoardSettings(context.Background(), "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	out := h.check()
	assert.Contains(t, out, "Waiting on YOU (Orchestrator), 1: Dev ", "a wait on the recipient is sent even with the all-clear off")

	// With everyone working, no wait and no tag, there is nothing to say and, with the all-clear off, nothing is sent.
	h2 := newCheckHarness(t)
	h2.member("Orchestrator", "Orchestrator", 1, 0)
	h2.member("Dev", "Developer", 2, 0)
	_, err = h2.s.UpdateBoardSettings(context.Background(), "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	h2.m.scan(context.Background())
	h2.mu.Lock()
	defer h2.mu.Unlock()
	assert.Empty(t, h2.sent["Orchestrator"])
}

// claimable tasks are the pending tasks nobody holds, not the assigned or parked ones.
func TestRosterCountsClaimableTasks(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Dev", "Developer", 2, 0)
	_, err := h.s.CreateTask(ctx, "team", "free one", "", "medium", "Orchestrator")
	require.NoError(t, err)
	_, err = h.s.CreateTask(ctx, "team", "free two", "", "medium", "Orchestrator")
	require.NoError(t, err)
	_, err = h.s.CreateTask(ctx, "team", "held", "", "medium", "Orchestrator", "Dev")
	require.NoError(t, err)
	parked, err := h.s.CreateTask(ctx, "team", "set aside", "", "medium", "Orchestrator")
	require.NoError(t, err)
	_, err = h.s.ParkTask(ctx, "team", parked.ID, "waiting", time.Time{})
	require.NoError(t, err)
	out := h.check()
	assert.Contains(t, out, "2 claimable tasks")
}
