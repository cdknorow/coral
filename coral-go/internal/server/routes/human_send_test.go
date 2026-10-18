package routes

import (
	"bytes"
	"context"
	"encoding/json"
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
