package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// #4819: an agy agent waiting on a tool permission prompt is waiting for the user, in its events. agy sends no hook for the prompt; the
// watcher of its pending tool confirms the prompt on the screen and records the permission_request Claude's hook sends. The screens are the
// ones the Antigravity CLI tester captured (agy 1.4.2).
const agyPermissionScreen = `Run this command?
> 1. Yes, run command
  2. Yes, and always allow in this conversation for commands that start with 'ls'
  3. Yes, and always allow for commands that start with 'ls' (Persist to settings.json)
  4. No, cancel

  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
esc to cancel`

type agyWatchRig struct {
	t      *testing.T
	h      *SessionsHandler
	term   *mockSessionTerminal
	sid    string
	name   string
	server string
	token  string
}

func newAgyWatchRig(t *testing.T, agentType string) *agyWatchRig {
	server, h, term, ss := setupSessionsTestServer(t)
	sid := "4819aaaa-0000-4000-8000-000000000001"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "agy-tester", AgentType: agentType, WorkingDir: "/tmp/t", SessionID: sid}))
	name := naming.SessionName(agentType, sid)
	term.addSession(name, "/tmp/t")
	return &agyWatchRig{t: t, h: h, term: term, sid: sid, name: name, server: server.URL, token: agentToken(t, h, sid, name)}
}

func (r *agyWatchRig) state() sessionstate.State {
	byID, err := r.h.ts.GetSessionStateEvents(context.Background(), []string{r.sid})
	require.NoError(r.t, err)
	in := SessionStateInput{}
	for _, e := range byID[r.sid] {
		in.Events = append(in.Events, e.StateEvent())
	}
	return sessionstate.Derive(in)
}

// waitingInList is the live list's waiting_for_input for the session: what the sidebar badge, the health check and the Inbox read.
func (r *agyWatchRig) waitingInList() bool {
	resp, err := http.Get(r.server + "/api/sessions/live")
	require.NoError(r.t, err)
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(r.t, json.NewDecoder(resp.Body).Decode(&rows))
	for _, row := range rows {
		if row["session_id"] == r.sid {
			w, _ := row["waiting_for_input"].(bool)
			return w
		}
	}
	r.t.Fatalf("session %s is not in the live list: %v", r.sid, rows)
	return false
}

func (r *agyWatchRig) event(typ, summary, tool, toolUseID string) {
	body := `{"event_type":"` + typ + `","summary":"` + summary + `","session_id":"` + r.sid + `","tool_name":"` + tool + `","tool_use_id":"` + toolUseID + `"}`
	r.post("/events", body)
}

func (r *agyWatchRig) post(path, body string) int {
	req, err := http.NewRequest("POST", r.server+"/api/sessions/live/"+r.name+path, bytes.NewBufferString(body))
	require.NoError(r.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.SessionTokenHeader, r.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	resp.Body.Close()
	return resp.StatusCode
}

func (r *agyWatchRig) pendTool(tool, toolUseID string) pendingTool {
	t := pendingTool{ToolUseID: toolUseID, ToolName: tool, At: time.Now()}
	r.h.pending.set(r.sid, t)
	return t
}

func TestAgyPermissionPromptIsRecordedAsAWaitAndAnswered(t *testing.T) {
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	tool := r.pendTool("run_command", "agy-step-2")
	assert.False(t, r.state().NeedsInput, "before anything is recorded the live state is the bug: in a turn, not waiting")

	// the tool is only slow: no prompt on the screen, nothing is recorded, and the watcher looks again
	r.term.setOutput(r.name, "$ make build\nbuilding...")
	assert.False(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1))
	assert.False(t, r.state().NeedsInput)

	// the permission prompt is on the screen: recorded, once
	r.term.setOutput(r.name, agyPermissionScreen)
	assert.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1))
	st := r.state()
	assert.True(t, st.NeedsInput, "waiting for the user")
	assert.True(t, st.OwnRequest)
	assert.True(t, r.waitingInList(), "the live list says waiting_for_input")
	assert.Equal(t, "permission", st.WaitingKind)
	assert.Contains(t, st.WaitingSummary, "run_command")
	assert.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1), "looked at again: nothing more to do")
	byID, _ := r.h.ts.GetSessionStateEvents(context.Background(), []string{r.sid})
	n := 0
	for _, e := range byID[r.sid] {
		if e.EventType == "permission_request" {
			n++
		}
	}
	assert.Equal(t, 1, n, "one request for one tool")

	// answered: the tool's PostToolUse clears it
	r.event("tool_use", "Ran: ls", "run_command", "agy-step-2")
	st = r.state()
	assert.False(t, st.NeedsInput, "answered")
	assert.True(t, st.InTurn)
	assert.False(t, r.waitingInList(), "and the live list stops saying so")
}

func TestAgyPermissionWaitEndsWithTheTurn(t *testing.T) {
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	tool := r.pendTool("run_command", "agy-step-3")
	r.term.setOutput(r.name, agyPermissionScreen)
	require.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1))
	require.True(t, r.state().NeedsInput)
	// declined: agy goes on or stops; the turn's end clears it, and an idle agent is awaiting the user, not waiting on a request
	r.event("stop", "Agent stopped: ok", "", "")
	st := r.state()
	assert.False(t, st.NeedsInput)
	assert.True(t, st.AwaitingUser, "idle at the end of a turn is awaiting the user")
	assert.False(t, r.waitingInList(), "awaiting the user is not waiting_for_input")
}

func TestAgyWatcherRecordsNothingWhenTheToolFinishedOrAnotherRequestIsOpen(t *testing.T) {
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	r.term.setOutput(r.name, agyPermissionScreen)
	// finished before the look: not pending any more
	tool := r.pendTool("run_command", "agy-step-4")
	r.h.pending.clear(r.sid, "agy-step-4")
	assert.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1))
	assert.False(t, r.state().NeedsInput, "a tool that finished is not a wait")
	// the turn moved on to another tool
	tool = r.pendTool("run_command", "agy-step-5")
	r.pendTool("view_file", "agy-step-6")
	assert.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", tool, 1))
	assert.False(t, r.state().NeedsInput)
	// a question the hook already recorded: no second request for the same tool, and its own PostToolUse answers it
	q := r.pendTool("ask_question", "agy-step-7")
	r.post("/events", `{"event_type":"permission_request","summary":"Waiting for the user: question (ask_question)","session_id":"`+r.sid+`","tool_name":"ask_question","tool_use_id":"agy-step-7","detail_json":{"kind":"question","tool_name":"ask_question"}}`)
	r.term.setOutput(r.name, "Question 1/1: Which database should we use?\n\n> 1. PostgreSQL\n  2. SQLite\n  3. Write-in...\n\n  ↑/↓ Navigate · enter Select · esc Skip\nesc to cancel")
	assert.True(t, r.h.confirmPromptWait(r.sid, r.name, at.Agy, "agy-tester", q, 1))
	assert.True(t, r.state().NeedsInput)
	assert.Equal(t, "question", r.state().WaitingKind)
	r.event("tool_use", "Asked", "ask_question", "agy-step-7")
	assert.False(t, r.state().NeedsInput, "the answer clears the one request")
}

func TestPendingToolStartsTheWatcherOfAnAgyAgentOnly(t *testing.T) {
	old := promptWatchDelays
	promptWatchDelays = []time.Duration{20 * time.Millisecond, 60 * time.Millisecond}
	t.Cleanup(func() { promptWatchDelays = old })
	agy := newAgyWatchRig(t, at.Agy)
	agy.event("prompt_submit", "User submitted prompt", "", "")
	agy.term.setOutput(agy.name, agyPermissionScreen)
	require.Equal(t, http.StatusOK, agy.post("/pending-tool", `{"session_id":"`+agy.sid+`","tool_use_id":"agy-step-9","tool_name":"run_command","tool_input":{"CommandLine":"ls"}}`))
	require.Eventually(t, func() bool { return agy.state().NeedsInput }, 3*time.Second, 20*time.Millisecond, "the agy agent's permission prompt is recorded")

	// Claude has its own hook for a permission prompt: its pending tool starts no watcher
	claude := newAgyWatchRig(t, at.Claude)
	claude.event("prompt_submit", "User submitted prompt", "", "")
	claude.term.setOutput(claude.name, agyPermissionScreen)
	require.Equal(t, http.StatusOK, claude.post("/pending-tool", `{"session_id":"`+claude.sid+`","tool_use_id":"toolu_1","tool_name":"Bash"}`))
	time.Sleep(300 * time.Millisecond)
	assert.False(t, claude.state().NeedsInput)
}

// #4819 (Lead's MUST): a watch is remembered only while it runs. After thousands of earlier tool calls a new watch still starts and records
// (the set once held every key for ever and refused new ones past 4096).
func TestWatcherStartedAfterManyEarlierOnesStillRecords(t *testing.T) {
	old := promptWatchDelays
	promptWatchDelays = []time.Duration{10 * time.Millisecond, 30 * time.Millisecond}
	t.Cleanup(func() { promptWatchDelays = old })
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	for i := 0; i < 4300; i++ { // tool calls that finish before the first look, one after another
		r.h.watchForPrompt(r.sid, r.name, pendingTool{ToolUseID: fmt.Sprintf("old-%d", i), ToolName: "view_file", At: time.Now()})
	}
	running := func() int {
		n := 0
		promptWatches.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	require.Eventually(t, func() bool { return running() == 0 }, 10*time.Second, 20*time.Millisecond, "a finished watch is forgotten")

	r.term.setOutput(r.name, agyPermissionScreen)
	tool := r.pendTool("run_command", "agy-step-after")
	r.h.watchForPrompt(r.sid, r.name, tool)
	require.Eventually(t, func() bool { return r.state().NeedsInput }, 3*time.Second, 20*time.Millisecond, "the 4301st watch records its prompt")
	require.Eventually(t, func() bool { return running() == 0 }, 3*time.Second, 20*time.Millisecond)
}

// Gemini's PreToolUse and prompt screen are not verified: its pending tool starts no watcher.
func TestGeminiPendingToolStartsNoWatcher(t *testing.T) {
	old := promptWatchDelays
	promptWatchDelays = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { promptWatchDelays = old })
	r := newAgyWatchRig(t, at.Gemini)
	r.event("prompt_submit", "User submitted prompt", "", "")
	r.term.setOutput(r.name, agyPermissionScreen)
	require.Equal(t, http.StatusOK, r.post("/pending-tool", `{"session_id":"`+r.sid+`","tool_use_id":"g-1","tool_name":"run_command"}`))
	time.Sleep(200 * time.Millisecond)
	assert.False(t, r.state().NeedsInput)
}

// #4874: the looks are every 500 ms for 6 s (a prompt agy drew at about 4 s was only seen by the last of three looks at 1.5, 3 and 5 s).
func TestPromptLooksAreEvery500msForSixSeconds(t *testing.T) {
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 1500 * time.Millisecond}, promptLooks(500*time.Millisecond, 1500*time.Millisecond))
	got := promptLooks(500*time.Millisecond, 6*time.Second)
	require.Len(t, got, 12)
	assert.Equal(t, 500*time.Millisecond, got[0])
	assert.Equal(t, 6*time.Second, got[len(got)-1])
	for i := 1; i < len(got); i++ {
		assert.Equal(t, 500*time.Millisecond, got[i]-got[i-1])
	}
	assert.Equal(t, got, promptWatchDelays, "the watcher's looks")
	assert.Equal(t, []time.Duration{1500 * time.Millisecond, 3 * time.Second, 5 * time.Second}, promptWatchCoarse, "a crowd of watchers keeps the old pace")
}

// A prompt drawn after the first looks is recorded by the next one, not only by the last: the screen is empty for the first looks, then shows the
// prompt, and the wait is on record long before the looks end. The looks span 5 s (10 ms apart) so a loaded machine cannot run past them, and the
// bound is the looks' own end: recorded within 2 s is "the next look", never "the end" (5 s).
func TestAgyPromptDrawnLaterIsRecordedByTheNextLook(t *testing.T) {
	old := promptWatchDelays
	promptWatchDelays = promptLooks(10*time.Millisecond, 5*time.Second)
	t.Cleanup(func() { promptWatchDelays = old })
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	r.term.setOutput(r.name, "thinking...") // nothing to answer yet
	tool := r.pendTool("run_command", "agy-step-late")
	r.h.watchForPrompt(r.sid, r.name, tool)
	time.Sleep(85 * time.Millisecond) // sleep-ok: the prompt is drawn after several looks; the looks last 5 s, so a late timer does not end them
	assert.False(t, r.state().NeedsInput, "nothing recorded while there is no prompt")
	start := time.Now()
	r.term.setOutput(r.name, agyPermissionScreen)
	require.Eventually(t, func() bool { return r.state().NeedsInput }, 10*time.Second, 5*time.Millisecond)
	assert.Less(t, time.Since(start), 2*time.Second, "recorded at the next look (10 ms apart), not at the end of the 5 s of looks")
}

// Which pace a watcher looks at is decided by promptLooksFor, and the decision is a pure function of how many watchers run, so it is tested
// as one, at the boundary, with no clock: up to promptWatchFine (itself included) the fine looks, past it the coarse ones.
func TestPromptLooksForTheCrowd(t *testing.T) {
	oldFine := promptWatchFine
	t.Cleanup(func() { promptWatchFine = oldFine })
	assert.Equal(t, int32(32), promptWatchFine, "32 fine watchers")
	assert.Equal(t, promptWatchDelays, promptLooksFor(1))
	assert.Equal(t, promptWatchDelays, promptLooksFor(promptWatchFine), "the 32nd watcher still looks at the fine pace")
	assert.Equal(t, promptWatchCoarse, promptLooksFor(promptWatchFine+1), "the 33rd looks at the old pace")
	assert.Equal(t, promptWatchCoarse, promptLooksFor(1000))
	promptWatchFine = 5
	assert.Equal(t, promptWatchDelays, promptLooksFor(4))
	assert.Equal(t, promptWatchDelays, promptLooksFor(5), "the boundary: the 5th of 5")
	assert.Equal(t, promptWatchCoarse, promptLooksFor(6))
	assert.NotEqual(t, promptWatchDelays, promptWatchCoarse, "the two paces differ, or the checks above prove nothing")
}

// A watcher in the crowd (every new watcher is past the fine limit here: promptWatchFine is 0, so the shared counter other tests' leftover
// watchers move does not matter) records its prompt at the COARSE look and only there, holds its slot until then, and gives it back. The fine
// looks are an hour apart, so only the coarse look can record (a watcher that ignored the cap would never record inside the test); the coarse look is
// 300 ms, so the slot is seen held (the counter above its value at the start) before the prompt is recorded, and seen given back after.
func TestCoarseWatcherStillRecordsAndGivesBackItsSlot(t *testing.T) {
	oldFine, oldCoarse, oldDelays := promptWatchFine, promptWatchCoarse, promptWatchDelays
	promptWatchFine, promptWatchCoarse, promptWatchDelays = 0, []time.Duration{300 * time.Millisecond}, []time.Duration{time.Hour}
	t.Cleanup(func() { promptWatchFine, promptWatchCoarse, promptWatchDelays = oldFine, oldCoarse, oldDelays })
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	r.term.setOutput(r.name, agyPermissionScreen)
	before := promptWatchActive.Load()
	tool := r.pendTool("run_command", "agy-step-crowd")
	r.h.watchForPrompt(r.sid, r.name, tool)
	require.Eventually(t, func() bool { return promptWatchActive.Load() > before }, 10*time.Second, 5*time.Millisecond, "the watcher holds a slot while it waits for its look")
	require.Eventually(t, func() bool { return r.state().NeedsInput }, 10*time.Second, 5*time.Millisecond, "the coarse watcher still records")
	require.Eventually(t, func() bool { return promptWatchActive.Load() <= before }, 10*time.Second, 5*time.Millisecond, "the watcher gave back the slot it took (the counter is at most what it was)")
}

// #4964: past promptWatchMax running watchers a pending tool starts none (a flood of distinct tool call ids cannot make a goroutine each),
// and a watcher that finishes frees its place.
func TestWatcherCountIsCapped(t *testing.T) {
	oldDelays, oldMax := promptWatchDelays, promptWatchMax
	promptWatchDelays = []time.Duration{400 * time.Millisecond}
	promptWatchMax = 2
	t.Cleanup(func() { promptWatchDelays, promptWatchMax = oldDelays, oldMax })
	r := newAgyWatchRig(t, at.Agy)
	r.event("prompt_submit", "User submitted prompt", "", "")
	running := func() int {
		n := 0
		promptWatches.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	for i := 0; i < 6; i++ {
		r.h.watchForPrompt(r.sid, r.name, r.pendTool("view_file", fmt.Sprintf("flood-%d", i)))
	}
	assert.Equal(t, 2, running(), "only promptWatchMax watchers run; the others were not started")
	assert.Equal(t, int32(2), promptWatchActive.Load())
	require.Eventually(t, func() bool { return running() == 0 && promptWatchActive.Load() == 0 }, 5*time.Second, 20*time.Millisecond, "the running ones finish and free their places")
	r.h.watchForPrompt(r.sid, r.name, r.pendTool("view_file", "after"))
	assert.Equal(t, 1, running(), "a new watch starts once there is room")
	require.Eventually(t, func() bool { return running() == 0 }, 5*time.Second, 20*time.Millisecond)
}
