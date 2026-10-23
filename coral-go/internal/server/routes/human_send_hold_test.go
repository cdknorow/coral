package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// #6610: a person's chat message is not typed into a box that asks the person. The agent here is a stand-in Claude whose hook events (its signals) say when a permission or question
// box is open and when it was answered; its terminal records what is typed.

type holdRig struct {
	t    *testing.T
	h    *SessionsHandler
	term *agentTerm
	sid  string
	name string
	post func(body string)
	send func(text string) *httptest.ResponseRecorder
}

func newHoldRig(t *testing.T, withSignals bool) *holdRig {
	t.Helper()
	server, h, mock, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "aaaaaaaa-6610-4000-8000-000000000001"
	name := naming.SessionName("claude", sid)
	mock.addSession(name, "/tmp/project")
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "chat", WorkingDir: "/tmp/project"}))
	require.NoError(t, ss.SetCLIVersion(ctx, sid, "2.1.300"))
	tok := agentToken(t, h, sid, name)
	term := &agentTerm{mockSessionTerminal: mock}
	h.terminal = term
	r := &holdRig{t: t, h: h, term: term, sid: sid, name: name}
	r.post = func(body string) {
		body = strings.Replace(body, "{", `{"hooks_rev":`+strconv.Itoa(agent.ClaudeHooksRev)+`,`, 1)
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/chat/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	// The agent takes a typed message: its UserPromptSubmit hook says so.
	term.onType = func(string) { r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`) }
	rt := chi.NewRouter()
	rt.Post("/api/sessions/live/{name}/send", h.Send)
	r.send = func(text string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"command": text, "agent_type": "claude", "session_id": sid})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/send", bytes.NewReader(b))
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}
	if withSignals {
		r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	} else {
		// An event its hooks did not send as a current set does (no hooks revision): the agent has started, and its signals say nothing about requests.
		id := sid
		_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &id, EventType: "stop", Summary: "Agent stopped: unknown"})
		require.NoError(t, err)
	}
	t.Cleanup(func() { // a message still held ends with its session, so no wait outlives the test
		r.post(`{"event_type":"session_end","summary":"Session ended: other","detail_json":{"reason":"other"}}`)
		require.Eventually(t, func() bool { return !r.h.holds.waiting(personKey(name, sid)) }, 5*time.Second, 10*time.Millisecond, "the holds ended")
	})
	oldS, oldP, oldHS := personHoldSettle, personHoldPoll, [5]time.Duration{humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait, humanSendSlowCommandWait}
	personHoldSettle, personHoldPoll = 150*time.Millisecond, 10*time.Millisecond
	humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait, humanSendSlowCommandWait = 400*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() {
		personHoldSettle, personHoldPoll = oldS, oldP
		humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait, humanSendSlowCommandWait = oldHS[0], oldHS[1], oldHS[2], oldHS[3], oldHS[4]
	})
	return r
}

func (r *holdRig) box(kind string) { // the agent shows a box that asks the person
	r.t.Helper()
	r.post(`{"event_type":"permission_request","summary":"Waiting for the user: ` + kind + ` (Bash)","tool_name":"Bash","detail_json":{"kind":"` + kind + `","tool_name":"Bash"}}`)
}
func (r *holdRig) answered() { // the person answered it
	r.t.Helper()
	// An approval is answered by its tool's PostToolUse; an elicitation by its own resolution.
	r.post(`{"event_type":"tool_use","summary":"Ran: ls","tool_name":"Bash"}`)
	r.post(`{"event_type":"request_resolved","summary":"Request answered","detail_json":{"kind":"elicitation"}}`)
}
func (r *holdRig) typed() []string { t, _ := r.term.counts(); return t }

func (r *holdRig) waitTyped(n int, within time.Duration) {
	r.t.Helper()
	require.Eventually(r.t, func() bool { return len(r.typed()) >= n }, within, 5*time.Millisecond, "typed: %v", r.typed())
}

func TestPersonMessageIsHeldWhileAnAgentBoxIsOpenAndSentOnceNoneIsLeft(t *testing.T) {
	for _, kind := range []string{"permission", "question", "plan", "elicitation"} {
		t.Run(kind, func(t *testing.T) {
			r := newHoldRig(t, true)
			r.box(kind)
			st, ok := r.h.gateState(context.Background(), r.name, "claude", r.sid)
			require.True(t, ok)
			require.Equal(t, ptymanager.StateBlocked, st.State, "the signals say it waits on a box")

			rec := r.send("deploy to staging")
			assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			var out map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			assert.Equal(t, true, out["held"])
			assert.Equal(t, heldNote, out["note"])

			time.Sleep(4 * personHoldSettle)
			assert.Empty(t, r.typed(), "nothing is typed into the box, however long it stays open")

			r.answered()
			r.waitTyped(1, 3*time.Second)
			assert.Equal(t, []string{"deploy to staging"}, r.typed(), "typed once, after the box was answered")
		})
	}
}

func TestPersonMessageWaitsThroughEveryBoxInARow(t *testing.T) {
	r := newHoldRig(t, true)
	r.box("permission")
	require.Equal(t, http.StatusAccepted, r.send("go on").Code)

	r.answered()
	time.Sleep(personHoldSettle / 3) // the next box opens right after the first one's answer, inside the settle time
	r.box("permission")
	time.Sleep(3 * personHoldSettle)
	assert.Empty(t, r.typed(), "the second box in the row holds it too")

	r.answered()
	time.Sleep(personHoldSettle / 3)
	r.box("question") // and a third
	time.Sleep(3 * personHoldSettle)
	assert.Empty(t, r.typed(), "and the third")

	r.answered()
	r.waitTyped(1, 3*time.Second)
	time.Sleep(2 * personHoldSettle)
	assert.Equal(t, []string{"go on"}, r.typed(), "sent once, when none was left")
}

func TestHeldPersonMessagesKeepTheirOrder(t *testing.T) {
	r := newHoldRig(t, true)
	r.box("permission")
	require.Equal(t, http.StatusAccepted, r.send("first").Code)
	require.Equal(t, http.StatusAccepted, r.send("second").Code)
	r.answered()
	r.waitTyped(2, 5*time.Second)
	assert.Equal(t, []string{"first", "second"}, r.typed())
}

func TestAMessageSentBehindAHeldOneKeepsItsPlaceEvenWhenNoBoxIsOpenNow(t *testing.T) {
	r := newHoldRig(t, true)
	r.box("permission")
	require.Equal(t, http.StatusAccepted, r.send("one").Code)
	r.answered() // the box is answered, but "one" is still inside its settle time
	rec := r.send("two")
	assert.Equal(t, http.StatusAccepted, rec.Code, "behind the held one, not typed ahead of it")
	r.waitTyped(2, 5*time.Second)
	assert.Equal(t, []string{"one", "two"}, r.typed())
}

func TestPersonMessageIsTypedAtOnceWhenNoBoxIsOpen(t *testing.T) {
	r := newHoldRig(t, true)
	rec := r.send("hello")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"hello"}, r.typed(), "as before")
	r.box("permission")
	r.answered()
	time.Sleep(50 * time.Millisecond)
	rec = r.send("after the answer")
	assert.NotEqual(t, http.StatusAccepted, rec.Code, "an answered box holds nothing")
}

func TestPersonMessageIsTypedAsBeforeWhenTheAgentHasNoSignals(t *testing.T) {
	r := newHoldRig(t, false) // no hook event yet: its signals can't say it waits on a box, and the screen alone does not hold a person's message
	rec := r.send("hello")
	assert.NotEqual(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"hello"}, r.typed())
}

func TestHeldPersonMessageIsRefusedWhenTheSessionEnds(t *testing.T) {
	r := newHoldRig(t, true)
	r.box("permission")
	var mu sync.Mutex
	var ended error
	got := make(chan struct{})
	held, err := r.h.deliverPersonSend(context.Background(), r.name, "claude", r.sid, "never typed", false, func(err error) { mu.Lock(); ended = err; mu.Unlock(); close(got) })
	require.NoError(t, err)
	require.True(t, held)
	r.post(`{"event_type":"session_end","summary":"Session ended: other","detail_json":{"reason":"other"}}`)
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("a held message of an ended session is answered")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.ErrorIs(t, ended, ptymanager.ErrTerminalGone)
	assert.Empty(t, r.typed())
}

func TestHeldPersonMessagesAreBounded(t *testing.T) {
	old := personHoldMaxPerAgent
	personHoldMaxPerAgent = 2
	t.Cleanup(func() { personHoldMaxPerAgent = old })
	r := newHoldRig(t, true)
	r.box("permission")
	assert.Equal(t, http.StatusAccepted, r.send("a").Code)
	assert.Equal(t, http.StatusAccepted, r.send("b").Code)
	rec := r.send("c")
	assert.Equal(t, http.StatusConflict, rec.Code, "over the bound it is refused, not queued without end")
	assert.Contains(t, rec.Body.String(), "agent_busy")
}

func TestHeldPersonMessageExpiresWhenTheBoxStaysOpen(t *testing.T) {
	old := personHoldMax
	personHoldMax = 200 * time.Millisecond
	t.Cleanup(func() { personHoldMax = old })
	r := newHoldRig(t, true)
	r.box("permission")
	got := make(chan error, 1)
	held, err := r.h.deliverPersonSend(context.Background(), r.name, "claude", r.sid, "too late", false, func(err error) { got <- err })
	require.NoError(t, err)
	require.True(t, held)
	select {
	case err := <-got:
		assert.Equal(t, errSendHeldExpired, err, "the person is told it was not sent")
	case <-time.After(3 * time.Second):
		t.Fatal("the hold has an end")
	}
	assert.Empty(t, r.typed())
}

// A box that was open when the message was held is not taken for gone because the signals cannot be read for a moment: the message goes on waiting.
func TestHeldPersonMessageKeepsWaitingWhenTheSignalsCannotBeReadNow(t *testing.T) {
	r := newHoldRig(t, true)
	r.box("permission")
	require.Equal(t, http.StatusAccepted, r.send("still waiting").Code)
	_, err := r.h.db.ExecContext(context.Background(), "DELETE FROM agent_events WHERE session_id = ?", r.sid)
	require.NoError(t, err)
	_, ok := r.h.gateState(context.Background(), r.name, "claude", r.sid)
	require.False(t, ok, "the signals are unreadable now")
	time.Sleep(5 * personHoldSettle)
	assert.Empty(t, r.typed(), "no evidence the box is gone: nothing is typed into it")
	assert.True(t, r.h.holds.waiting(personKey(r.name, r.sid)), "and it is still held: no delivery was even tried")
}

// #6615: a Codex whose hooks include PermissionRequest raises its row's Needs input at its approval dialog, and the tool's own PostToolUse clears it: so a person's chat message is held behind
// the dialog for Codex too.
func TestCodexApprovalDialogRaisesNeedsInputAndHoldsAPersonsMessage(t *testing.T) {
	server, h, mock, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "cccccccc-6615-4000-8000-000000000001"
	name := naming.SessionName("codex", sid)
	mock.addSession(name, "/tmp/project")
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "cx", WorkingDir: "/tmp/project"}))
	tok := agentToken(t, h, sid, name)
	post := func(body string) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/cx/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	needs := func() bool { return h.deriveSingleSessionState(ctx, sid, 0).NeedsInput }
	assert.False(t, needs())

	post(`{"event_type":"permission_request","summary":"Waiting for the user: permission (Bash)","tool_name":"Bash","detail_json":{"kind":"permission","tool_name":"Bash"}}`)
	assert.True(t, needs(), "the approval dialog is Needs input")
	st, ok := h.gateState(ctx, name, "codex", sid)
	require.True(t, ok)
	assert.Equal(t, ptymanager.StateBlocked, st.State)
	assert.Equal(t, "permission", st.Kind)
	assert.True(t, h.blockedOnBox(ctx, name, "codex", sid), "a person's message is held behind it")

	post(`{"event_type":"tool_use","summary":"Ran: ls","tool_name":"Bash"}`)
	assert.False(t, needs(), "approved: the tool ran")
	assert.False(t, h.blockedOnBox(ctx, name, "codex", sid))
}

// Security's SHOULD on #6610: a message that came through Coral Hub waited with the authority of a viewer at Interact; if this computer's mode is lowered to View while it waits, it is
// refused at release, not typed. A message from the dashboard is not affected by the mode.
func TestHeldHubMessageIsRefusedWhenTheComputersModeIsLoweredWhileItWaits(t *testing.T) {
	r := newHoldRig(t, true)
	ctx := context.Background()
	ss := store.NewSessionStore(r.h.db)
	require.NoError(t, ss.SetSetting(ctx, MaxModeSetting, "interact"))
	r.box("permission")
	fromHub := make(chan error, 1)
	held, err := r.h.deliverPersonSend(ctx, r.name, "claude", r.sid, "from the hub", true, func(err error) { fromHub <- err })
	require.NoError(t, err)
	require.True(t, held)
	fromDash := make(chan error, 1)
	held, err = r.h.deliverPersonSend(ctx, r.name, "claude", r.sid, "from the dashboard", false, func(err error) { fromDash <- err })
	require.NoError(t, err)
	require.True(t, held)

	require.NoError(t, ss.SetSetting(ctx, MaxModeSetting, "view")) // the owner lowers the computer's mode
	r.answered()
	select {
	case err := <-fromHub:
		assert.Equal(t, errSendModeLowered, err, "refused, and told why")
	case <-time.After(5 * time.Second):
		t.Fatal("the hub message is answered")
	}
	select {
	case err := <-fromDash:
		assert.NoError(t, err, "a person at the dashboard is not a Hub viewer")
	case <-time.After(5 * time.Second):
		t.Fatal("the dashboard message is answered")
	}
	assert.Equal(t, []string{"from the dashboard"}, r.typed(), "only the dashboard's message was typed")
}

func TestHeldHubMessageIsTypedWhenTheModeStillAllowsInteract(t *testing.T) {
	r := newHoldRig(t, true)
	ctx := context.Background()
	require.NoError(t, store.NewSessionStore(r.h.db).SetSetting(ctx, MaxModeSetting, "interact"))
	r.box("permission")
	got := make(chan error, 1)
	held, err := r.h.deliverPersonSend(ctx, r.name, "claude", r.sid, "from the hub", true, func(err error) { got <- err })
	require.NoError(t, err)
	require.True(t, held)
	r.answered()
	select {
	case err := <-got:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("answered")
	}
	assert.Equal(t, []string{"from the hub"}, r.typed())
}

// A held message counts once toward the bound, whether it is still in the agent's queue or taken off it by the release loop: the gate gave 2 for one message in the queue (the message and the
// loop about to take it), so a bound of 2 refused a second message that fit (the CI run of #6610). The states are set directly: which one a real run is in depends on a goroutine's timing.
func TestAHeldMessageCountsOnceTowardTheBound(t *testing.T) {
	p := &personHolds{queues: map[string][]*personHeld{}, running: map[string]bool{}, releasing: map[string]bool{}}
	const key = "agent"
	assert.Equal(t, 0, p.held(key), "nothing held")

	p.queues[key] = []*personHeld{{text: "a"}} // queued, the loop started and not yet at it
	p.running[key] = true
	assert.Equal(t, 1, p.held(key), "one message in the queue is one")

	p.queues[key] = nil // the loop took it and waits for its turn
	p.releasing[key] = true
	assert.Equal(t, 1, p.held(key), "taken off the queue it is still one")

	p.queues[key] = []*personHeld{{text: "b"}} // and a second queued behind it
	assert.Equal(t, 2, p.held(key))

	p.running[key] = false
	delete(p.releasing, key)
	p.queues[key] = nil
	assert.Equal(t, 0, p.held(key), "the loop alone counts none")
}

// A bound of 2 takes two messages however fast they come, and refuses the third: the real run, many times over, with no wait between the sends.
func TestTheBoundOfTwoTakesTwoMessagesHoweverFastTheyCome(t *testing.T) {
	old := personHoldMaxPerAgent
	personHoldMaxPerAgent = 2
	t.Cleanup(func() { personHoldMaxPerAgent = old })
	for i := 0; i < 15; i++ {
		r := newHoldRig(t, true)
		r.box("permission")
		a, b, c := r.send("a"), r.send("b"), r.send("c")
		assert.Equal(t, http.StatusAccepted, a.Code, "run %d: a", i)
		assert.Equal(t, http.StatusAccepted, b.Code, "run %d: b", i)
		assert.Equal(t, http.StatusConflict, c.Code, "run %d: c is over the bound", i)
		r.post(`{"event_type":"session_end","summary":"Session ended: other","detail_json":{"reason":"other"}}`)
		require.Eventually(t, func() bool { return !r.h.holds.waiting(personKey(r.name, r.sid)) }, 5*time.Second, 5*time.Millisecond)
	}
}
