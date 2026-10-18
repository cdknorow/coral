package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// agentTerm is a stand-in agent terminal for a person's message (#4587): its screen, what a typed message and an
// Enter do to it, and what was typed and pressed.
type agentTerm struct {
	*mockSessionTerminal
	mu      sync.Mutex
	screen  string
	typed   []string
	enters  int
	onType  func(text string) // what the agent's screen does with typed text and its Enter
	onEnter func()            // what an Enter pressed again does
}

func (a *agentTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.screen, nil
}

func (a *agentTerm) show(s string) { a.mu.Lock(); a.screen = s; a.mu.Unlock() }

func (a *agentTerm) SendInput(_ context.Context, _, text, _, _ string) error {
	a.mu.Lock()
	a.typed = append(a.typed, text)
	f := a.onType
	a.mu.Unlock()
	if f != nil {
		f(text)
	}
	return nil
}

func (a *agentTerm) SendEnter(context.Context, string, string, string) error {
	a.mu.Lock()
	a.enters++
	f := a.onEnter
	a.mu.Unlock()
	if f != nil {
		f()
	}
	return nil
}

func (a *agentTerm) counts() ([]string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.typed...), a.enters
}

// Codex 0.155.1's screen when ready (a real capture), with its input box empty or holding text.
func codexReady(t *testing.T, inBox string) string {
	s := codexHookTrust(t, "tmux_trusted.txt")
	if inBox == "" {
		return s
	}
	return strings.Replace(s, "› Ask Codex to do anything", "› "+inBox, 1)
}

func TestHumanSendIsSubmittedOrSaysSo(t *testing.T) {
	// submit records the agent's prompt_submit event, as its UserPromptSubmit hook does when it takes a prompt.
	type harness struct {
		term   *agentTerm
		send   func(string) *httptest.ResponseRecorder
		submit func()
	}
	setup := func(t *testing.T, agentType string, withEvent bool) harness {
		_, handler, mock, ss := setupSessionsTestServer(t)
		sid := "eeeeeeee-4587-4000-8000-000000000001"
		name := naming.SessionName(agentType, sid)
		mock.addSession(name, "/tmp/project")
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: "chat", WorkingDir: "/tmp/project"}))
		event := func(typ string) {
			_, err := handler.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: typ, Summary: typ})
			require.NoError(t, err)
		}
		if withEvent {
			event("stop")
		}
		term := &agentTerm{mockSessionTerminal: mock}
		handler.terminal = term
		old := []any{humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait}
		humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait = 400*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond, 80*time.Millisecond
		t.Cleanup(func() {
			humanSendReadyWait, humanSendPollEvery, humanSendEnterWait, humanSendConfirmWait = old[0].(time.Duration), old[1].(time.Duration), old[2].(time.Duration), old[3].(time.Duration)
		})
		r := chi.NewRouter()
		r.Post("/api/sessions/live/{name}/send", handler.Send)
		return harness{term: term, submit: func() { event("prompt_submit") }, send: func(text string) *httptest.ResponseRecorder {
			b, _ := json.Marshal(map[string]any{"command": text, "agent_type": agentType, "session_id": sid})
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/send", bytes.NewReader(b))
			req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			return rec
		}}
	}
	code := func(rec *httptest.ResponseRecorder) string {
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		s, _ := out["code"].(string)
		return s
	}

	t.Run("codex: the first Enter is lost while it starts; it is pressed again and the message submitted", func(t *testing.T) {
		h := setup(t, "codex", false)
		term := h.term
		term.show(codexReady(t, ""))
		term.onType = func(text string) { term.show(codexReady(t, text)) } // the text lands, its Enter is lost
		term.onEnter = func() { term.show(codexReady(t, "")) }             // the next Enter submits it
		rec := h.send("hello there")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := term.counts()
		assert.Equal(t, []string{"hello there"}, typed, "typed once")
		assert.Equal(t, 1, enters, "one Enter more")
	})
	t.Run("codex: never submitted: not 'sent'", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, ""))
		term.onType = func(text string) { term.show(codexReady(t, text)) }
		rec := h.send("stuck message")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_submitted", code(rec))
		_, enters := term.counts()
		assert.Equal(t, humanSendExtraEnters, enters, "Enter pressed again a bounded number of times")
	})
	t.Run("codex: the same message sent again while it sits in the box: Enter, not a second copy", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, "stuck message"))
		term.onEnter = func() { term.show(codexReady(t, "")); h.submit() }
		rec := h.send("stuck message")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := term.counts()
		assert.Empty(t, typed, "not typed again")
		assert.Equal(t, 1, enters)
	})
	t.Run("codex: a different message with one in the box is typed (and the box is not taken for it)", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, "stuck message"))
		term.onType = func(text string) { h.submit() }
		rec := h.send("stuck")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := term.counts()
		assert.Equal(t, []string{"stuck"}, typed)
		assert.Zero(t, enters)
	})
	t.Run("codex: no input box yet while it starts: the message waits for it", func(t *testing.T) {
		h := setup(t, "codex", false)
		term := h.term
		term.show("")
		term.onType = func(text string) { h.submit() } // submitted at once: the box never shows it
		time.AfterFunc(60*time.Millisecond, func() { term.show(codexReady(t, "")) })
		rec := h.send("first prompt")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := term.counts()
		assert.Equal(t, []string{"first prompt"}, typed)
		assert.Zero(t, enters)
	})
	t.Run("codex: never ready: refused, nothing typed", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show("")
		rec := h.send("too early")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "agent_not_ready", code(rec))
		typed, _ := h.term.counts()
		assert.Empty(t, typed)
	})
	t.Run("codex: its keystrokes lost while it starts: typed again, and sent once it takes them", func(t *testing.T) {
		h := setup(t, "codex", false)
		term := h.term
		term.show(codexReady(t, ""))
		n := 0
		term.onType = func(text string) { // the first typing never reaches the box; the second is submitted
			if n++; n == 2 {
				h.submit()
			}
		}
		rec := h.send("hello")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := term.counts()
		assert.Equal(t, []string{"hello", "hello"}, typed)
		assert.Zero(t, enters)
	})
	t.Run("codex: its keystrokes always lost: not 'sent', typed again a bounded number of times", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		rec := h.send("hello")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
		typed, enters := h.term.counts()
		assert.Len(t, typed, 1+humanSendRetypes)
		assert.Zero(t, enters)
	})
	t.Run("codex: a running agent that shows nothing and says nothing: not 'sent', and not typed again", func(t *testing.T) {
		h := setup(t, "codex", true)
		h.term.show(codexReady(t, ""))
		rec := h.send("hello")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1, "a running agent may have taken it: never typed twice")
	})
	t.Run("codex: a short message the placeholder contains is not taken as in the box", func(t *testing.T) {
		h := setup(t, "codex", true)
		h.term.show(codexReady(t, "")) // "Ask Codex to do anything"
		h.term.onType = func(text string) { h.submit() }
		rec := h.send("do")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := h.term.counts()
		assert.Equal(t, []string{"do"}, typed, "typed, not taken as already there")
		assert.Zero(t, enters)
	})
	t.Run("a menu on screen: typed as before (a person may answer it), nothing more", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexHookTrust(t, "tmux_open.txt"))
		h.term.onType = func(string) { h.term.show(codexReady(t, "")) } // the answer closes the menu
		rec := h.send("2")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := h.term.counts()
		assert.Equal(t, []string{"2"}, typed)
		assert.Zero(t, enters)
	})
	t.Run("two messages at once: one at a time through the box, each acknowledged for its own submission", func(t *testing.T) {
		// Security's probe on #4587: two sends 25 ms apart to an agent that takes typed text and loses the first
		// Enter of each message. Without the gate both were typed into one box and both said "sent".
		h := setup(t, "codex", true)
		term := h.term
		var mu sync.Mutex
		box, enters, submitted, merged := "", 0, []string{}, false
		typedAfter := map[string]int{} // a message -> how many had been submitted when it was typed
		term.show(codexReady(t, ""))
		term.onType = func(text string) {
			mu.Lock()
			if box != "" {
				merged = true
			}
			typedAfter[text] = len(submitted)
			box += text
			enters = 0
			b := box
			mu.Unlock()
			term.show(codexReady(t, b)) // its Enter is lost
		}
		term.onEnter = func() {
			mu.Lock()
			if enters++; enters == 1 || box == "" {
				mu.Unlock()
				return // lost
			}
			submitted = append(submitted, box)
			box = ""
			mu.Unlock()
			term.show(codexReady(t, ""))
			h.submit()
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, text := range []string{"deploy to staging", "rm -rf the build"} {
			wg.Add(1)
			go func() { defer wg.Done(); codes[i] = h.send(text).Code }()
			time.Sleep(25 * time.Millisecond) // sleep-ok: the probe's gap between the two sends
		}
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		assert.False(t, merged, "never both messages in the box")
		assert.Equal(t, []string{"deploy to staging", "rm -rf the build"}, submitted, "each submitted on its own")
		assert.Equal(t, 1, typedAfter["rm -rf the build"], "the second typed only after the first was submitted")
		assert.Equal(t, []int{http.StatusOK, http.StatusOK}, codes)
	})
	t.Run("a message left unsubmitted: a different one is refused untyped, the same one gets Enter", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, ""))
		term.onType = func(text string) { term.show(codexReady(t, text)) } // every Enter lost
		rec := h.send("deploy to staging")
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "not_submitted", code(rec))
		rec = h.send("rm -rf the build")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "box_not_empty", code(rec))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		assert.Equal(t, true, out["not_sent"])
		typed, _ := term.counts()
		assert.Equal(t, []string{"deploy to staging"}, typed, "the second never typed after the first")
		term.onEnter = func() { term.show(codexReady(t, "")); h.submit() }
		rec = h.send("deploy to staging")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, _ = term.counts()
		assert.Equal(t, []string{"deploy to staging"}, typed, "Enter, not a second copy")
		rec = h.send("rm -rf the build") // the box is clear now
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
	t.Run("a leftover cleared in the terminal no longer blocks", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, ""))
		term.onType = func(text string) { term.show(codexReady(t, text)) }
		require.Equal(t, "not_submitted", code(h.send("first")))
		term.show(codexReady(t, "")) // a person cleared the box
		term.onType = func(string) { h.submit() }
		rec := h.send("second")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
	t.Run("claude: submitted by its own Enter: no Enter more", func(t *testing.T) {
		h := setup(t, "claude", true)
		ready := claudeUnnumbered(t, "ready.txt")
		h.term.show(ready)
		h.term.onType = func(text string) { h.submit() }
		rec := h.send("do the thing")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, enters := h.term.counts()
		assert.Zero(t, enters)
	})
}

// The gate bounds the messages waiting for one agent and in all, and a cancelled wait gives up its place (#4587).
func TestHumanSendGateBoundsAndCancel(t *testing.T) {
	var g humanSendGate
	ctx := context.Background()
	leave, err := g.enter(ctx, "a")
	require.NoError(t, err)
	type result struct {
		leave func()
		err   error
	}
	waiting := make(chan result, humanSendMaxWaiting)
	for i := 0; i < humanSendMaxWaiting; i++ {
		go func() { l, err := g.enter(ctx, "a"); waiting <- result{l, err} }()
	}
	require.Eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.agents["a"].count == 1+humanSendMaxWaiting }, time.Second, time.Millisecond)
	_, err = g.enter(ctx, "a")
	assert.ErrorIs(t, err, errSendBusy, "a fifth waiter for one agent is refused at once")
	l2, err := g.enter(ctx, "b")
	require.NoError(t, err, "another agent is not held up")
	l2()

	// A cancelled waiter gives up its place, and a new one fits again.
	cctx, cancel := context.WithCancel(ctx)
	leave() // one waiter takes the turn; a place frees
	r := <-waiting
	require.NoError(t, r.err)
	got := make(chan error, 1)
	go func() { _, err := g.enter(cctx, "a"); got <- err }()
	require.Eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.agents["a"].count == 1+humanSendMaxWaiting }, time.Second, time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-got, context.Canceled)
	g.mu.Lock()
	assert.Equal(t, humanSendMaxWaiting, g.agents["a"].count, "its place is given back")
	g.mu.Unlock()
	r.leave()
	for i := 0; i < humanSendMaxWaiting-1; i++ {
		r := <-waiting
		require.NoError(t, r.err)
		r.leave()
	}
	g.mu.Lock()
	assert.Empty(t, g.agents, "nothing kept once every turn ended")
	assert.Zero(t, g.inFlight)
	g.mu.Unlock()

	// In all: humanSendMaxInFlight under way across agents, then refused.
	var leaves []func()
	for i := 0; i < humanSendMaxInFlight; i++ {
		l, err := g.enter(ctx, fmt.Sprintf("agent-%d", i))
		require.NoError(t, err)
		leaves = append(leaves, l)
	}
	_, err = g.enter(ctx, "one-more")
	assert.ErrorIs(t, err, errSendBusy)
	for _, l := range leaves {
		l()
	}
	_, err = g.enter(ctx, "one-more")
	assert.NoError(t, err)
}
