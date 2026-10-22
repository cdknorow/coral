package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/terminalinput"
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

func (a *agentTerm) SendInput(_ context.Context, _ terminalinput.Origin, _, text, _, _ string) error {
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
		event  func(typ string) // any event of the agent's (a /clear's SessionStart records session_reset)
		hints  *promptHints
		sid    string
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
		return harness{term: term, hints: &handler.hints, sid: sid, submit: func() { event("prompt_submit") }, event: event, send: func(text string) *httptest.ResponseRecorder {
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
	t.Run("a command stuck in the box is not 'sent' because the agent recorded some other event, and nothing is typed after it", func(t *testing.T) {
		h := setup(t, "codex", true)
		term := h.term
		term.show(codexReady(t, ""))
		// The text lands and stays (its Enters do nothing) while the agent, busy with its own work, records a tool_use.
		term.onType = func(text string) { term.show(codexReady(t, text)); h.event("tool_use") }
		rec := h.send("/compact")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_submitted", code(rec), "an event is not evidence for a command that is still in the box")
		term.onType = func(text string) { h.submit() }
		rec = h.send("hello after")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "box_not_empty", code(rec), "refused untyped, not typed after the stuck command")
		typed, _ := term.counts()
		assert.Equal(t, []string{"/compact"}, typed, "only the command was ever typed")
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
			if i == 0 {
				// The second send comes while the first is in the box, its Enter lost: not "25 ms later", which a loaded or -race host can stretch
				// until the second goroutine runs first and the expected order is wrong (#4979).
				require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); _, typed := typedAfter[text]; return typed }, 20*time.Second, 2*time.Millisecond, "the first message is typed")
			}
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

	// Slash commands (#4705): the CLI's own, with no UserPromptSubmit; its Enter takes the command out of the box
	// before the screen is read. Stand-in screens from the real ready captures.
	claude := func(inBox string) string {
		ready := claudeUnnumbered(t, "ready.txt")
		const placeholder = "❯\u00a0Try \"fix typecheck errors\"" // Claude draws a no-break space after its ❯
		require.Contains(t, ready, placeholder)
		return strings.Replace(ready, placeholder, "❯\u00a0"+inBox, 1)
	}
	agy := func(t *testing.T, inBox string) string {
		b, err := os.ReadFile("../../ptymanager/testdata/screens/agy/1.3.5/idle.ansi")
		require.NoError(t, err)
		s := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(string(b), "")
		box := regexp.MustCompile(`(?m)^>[ \t]*$`)
		require.True(t, box.MatchString(s), "agy's empty input box")
		return box.ReplaceAllLiteralString(s, "> "+inBox)
	}
	compacting := func(screen string) string { // working on it: a status line above the (empty) box
		return strings.Replace(screen, "\n\n", "\n\n✻ Compacting conversation… (esc to interrupt)\n", 1)
	}
	const picker = "\n Select model\n Switch between models. Applies to this session.\n\n ❯ 1. Default (recommended)\n   2. Sonnet\n   3. Haiku\n\n Enter to confirm · Esc to exit\n"
	for _, c := range []struct {
		name, agentType string
		ready           func(t *testing.T, inBox string) string
	}{
		{"claude", "claude", func(_ *testing.T, inBox string) string { return claude(inBox) }},
		{"codex", "codex", codexReady},
		{"agy", "agy", agy},
	} {
		t.Run(c.name+": /compact runs with no event and leaves the box at once: sent", func(t *testing.T) {
			h := setup(t, c.agentType, true)
			h.term.show(c.ready(t, ""))
			h.term.onType = func(string) { h.term.show(compacting(c.ready(t, ""))) }
			rec := h.send("/compact")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			typed, enters := h.term.counts()
			assert.Equal(t, []string{"/compact"}, typed, "typed once")
			assert.Zero(t, enters, "no Enter more")
		})
		t.Run(c.name+": /clear: its SessionStart is the evidence, at once", func(t *testing.T) {
			h := setup(t, c.agentType, true)
			humanSendConfirmWait = 2 * time.Second // what a command with no evidence waits: this one doesn't
			h.term.show(c.ready(t, ""))
			h.term.onType = func(string) { h.event("session_reset") }
			start := time.Now()
			rec := h.send("/clear")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Less(t, time.Since(start), time.Second, "confirmed by the event, not by the end of the wait")
			typed, enters := h.term.counts()
			assert.Equal(t, []string{"/clear"}, typed)
			assert.Zero(t, enters)
		})
		if c.agentType == "claude" {
			// #5488: /model <name> on a conversation that has a cache opens a confirmation dialog, which sends no hook event: the chat must read the screen for it for a while, and the dialog is a numbered prompt the chat can answer.
			t.Run(c.name+": /model <name> opens its Switch model? dialog: sent at once, and the prompt is looked for on the screen", func(t *testing.T) {
				h := setup(t, c.agentType, true)
				humanSendConfirmWait = 2 * time.Second
				h.term.show(c.ready(t, ""))
				const dialog = "\n Switch model?\n Your next response will be slower and use more tokens\n\n This conversation is cached for the current model. Switching to Sonnet 4.5 means the full history gets re-read on your next message.\n\n ❯ 1. Yes, switch to Sonnet 4.5\n   2. No, go back\n"
				h.term.onType = func(string) { h.term.show(dialog) }
				h.hints.note(h.sid, "stop") // the agent's last hook event says no prompt is open
				require.False(t, h.hints.mayBeOpen(h.sid))
				rec := h.send("/model claude-sonnet-4-5")
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.True(t, h.hints.mayBeOpen(h.sid), "after a slash command typed by the Operator the screen is read for a prompt")
				ps, ok := parsePromptScreen(dialog)
				require.True(t, ok, "the dialog is a numbered prompt")
				require.Len(t, ps.Options, 2)
				assert.Equal(t, "Yes, switch to Sonnet 4.5", ps.Options[0].Label)
				assert.True(t, ps.Options[0].Selected)
				assert.Equal(t, "No, go back", ps.Options[1].Label)
			})
			t.Run(c.name+": a message that is not a slash command does not open the window", func(t *testing.T) {
				h := setup(t, c.agentType, true)
				h.term.show(c.ready(t, ""))
				h.term.onType = func(string) { h.submit() }
				h.hints.note(h.sid, "stop")
				rec := h.send("please do the thing")
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.False(t, h.hints.mayBeOpen(h.sid))
			})
		}
		t.Run(c.name+": /model opens its picker: sent at once, and no Enter into the picker", func(t *testing.T) {
			h := setup(t, c.agentType, true)
			humanSendConfirmWait = 2 * time.Second
			h.term.show(c.ready(t, ""))
			h.term.onType = func(string) { h.term.show(picker) }
			start := time.Now()
			rec := h.send("/model")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Less(t, time.Since(start), time.Second, "confirmed by the picker, not by the end of the wait")
			typed, enters := h.term.counts()
			assert.Equal(t, []string{"/model"}, typed)
			assert.Zero(t, enters, "an Enter would pick a model")
		})
		t.Run(c.name+": a command left in the box: not submitted, as a message", func(t *testing.T) {
			h := setup(t, c.agentType, true)
			h.term.show(c.ready(t, ""))
			h.term.onType = func(text string) { h.term.show(c.ready(t, text)) }
			rec := h.send("/compact")
			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "not_submitted", code(rec))
			_, enters := h.term.counts()
			assert.Equal(t, humanSendExtraEnters, enters)
		})
	}
	t.Run("a command to an agent still starting whose keystrokes are lost: never typed again, not confirmed", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		rec := h.send("/compact")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_confirmed", code(rec))
		typed, enters := h.term.counts()
		assert.Equal(t, []string{"/compact"}, typed, "/compact acts on the agent: it is typed once")
		assert.Zero(t, enters)
	})
	// An agent with no hook events (its hooks do not reach Coral, or it has none yet) sends nothing for a taken message: its box is
	// empty again and its screen has moved on. That is "sent", once (it was typed three times and then answered 409).
	for _, msg := range []string{"hello there", "/clear"} {
		t.Run("no hook events, the box emptied after the typing: "+msg+" is sent once", func(t *testing.T) {
			h := setup(t, "codex", false)
			h.term.show(codexReady(t, ""))
			h.term.onType = func(text string) { h.term.show("> you: " + text + "\n" + codexReady(t, "")) }
			rec := h.send(msg)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			typed, _ := h.term.counts()
			assert.Equal(t, []string{msg}, typed)
		})
	}
	// Security's probe on #73: a screen that redraws by itself (a spinner) is no evidence that the message arrived.
	t.Run("no hook events, a spinner redraws and the text never shows: typed again once, then not confirmed, never sent", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		h.term.onType = func(string) { h.term.show("Working (3s)\n" + codexReady(t, "")) }
		rec := h.send("please fix the build")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_confirmed", code(rec))
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1+humanSendRetypes)
	})
	t.Run("no hook events, the first typing is lost behind a spinner and the second lands: sent after one retype", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		n := 0
		h.term.onType = func(text string) {
			if n++; n == 1 {
				h.term.show("Working (3s)\n" + codexReady(t, ""))
				return
			}
			h.term.show("> you: " + text + "\n" + codexReady(t, ""))
		}
		rec := h.send("please fix the build")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, _ := h.term.counts()
		assert.Len(t, typed, 2)
	})
	t.Run("no hook events, a long multi-line message echoed wrapped: its first line is found, sent once", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		msg := "Please review the change to the retry logic in the uploader, and tell me whether the backoff can overflow"+"\n"+"Second line with more detail."
		h.term.onType = func(text string) {
			h.term.show("> Please review the change to the retry\n  logic in the uploader, and tell me whether\n  the backoff can overflow Second line" + "\n" + codexReady(t, ""))
		}
		rec := h.send(msg)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1)
	})
	t.Run("an earlier message of the same words on the screen is no echo of this one", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show("> you: please fix the build\n" + codexReady(t, ""))
		h.term.onType = func(string) {}
		rec := h.send("please fix the build")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1+humanSendRetypes)
	})
	t.Run("no hook events, a text too short to search for: a screen that moved is taken as sent", func(t *testing.T) {
		h := setup(t, "codex", false)
		h.term.show(codexReady(t, ""))
		h.term.onType = func(string) { h.term.show("Working (3s)\n" + codexReady(t, "")) }
		rec := h.send("ok")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1)
	})
	t.Run("a message that starts with a path is a message: no evidence, not confirmed", func(t *testing.T) {
		h := setup(t, "codex", true)
		h.term.show(codexReady(t, ""))
		rec := h.send("/tmp/build.log is missing")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
	})
	t.Run("a menu after a message doesn't confirm it (the message may not have opened it)", func(t *testing.T) {
		h := setup(t, "codex", true)
		h.term.show(codexReady(t, ""))
		h.term.onType = func(string) { h.term.show(picker) }
		rec := h.send("hello")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
	})
	t.Run("a message's own events other than prompt_submit don't confirm it", func(t *testing.T) {
		h := setup(t, "codex", true)
		h.term.show(codexReady(t, ""))
		h.term.onType = func(string) { h.event("tool_use") }
		rec := h.send("hello")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
	})

	// #4794: a Chat send with attached images is "<upload path> ... <text>". The CLI may show each image as its own
	// placeholder in the box, read the images before its UserPromptSubmit hook, or queue the message behind its turn.
	attach := func(t *testing.T) (img func(name string) string) {
		old, oldWait := uploadDir, humanSendAttachConfirmWait
		uploadDir, humanSendAttachConfirmWait = t.TempDir()+"/Coral Data/uploads", 600*time.Millisecond // a space in the data dir's path
		t.Cleanup(func() { uploadDir, humanSendAttachConfirmWait = old, oldWait })
		return func(name string) string { return filepath.ToSlash(uploadDir) + "/" + name }
	}
	ready := func(string) string { return claudeUnnumbered(t, "ready.txt") } // the empty box, with its placeholder
	t.Run("claude: an image alone, its hook after the image is read: sent", func(t *testing.T) {
		h := setup(t, "claude", true)
		img := attach(t)
		h.term.show(ready(""))
		h.term.onType = func(string) {
			h.term.show(claude("[Image #1]"))
			go func() { time.Sleep(300 * time.Millisecond); h.term.show(ready("")); h.submit() }()
		}
		rec := h.send(img("screenshot_20261018_102600_ab12.png"))
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
	t.Run("claude: one image and text, its hook after the image is read: sent", func(t *testing.T) {
		h := setup(t, "claude", true)
		img := attach(t)
		text := img("1a2b3c4d_shot.png") + " lets look at this"
		h.term.show(ready(""))
		h.term.onType = func(string) { go func() { time.Sleep(300 * time.Millisecond); h.submit() }() } // taken at once
		rec := h.send(text)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, enters := h.term.counts()
		assert.Equal(t, []string{text}, typed, "typed once")
		assert.Zero(t, enters)
	})
	t.Run("claude: two images and text: sent", func(t *testing.T) {
		h := setup(t, "claude", true)
		img := attach(t)
		h.term.show(ready(""))
		h.term.onType = func(string) { go func() { time.Sleep(400 * time.Millisecond); h.submit() }() }
		rec := h.send(img("aaaa1111_a.png") + " " + img("bbbb2222_b.png") + " which one is right")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
	t.Run("claude: an image and text to an agent in the middle of a turn: queued, sent", func(t *testing.T) {
		h := setup(t, "claude", true)
		h.event("tool_use") // working
		img := attach(t)
		h.term.show(ready("")) // the CLI takes it at once and holds it for the turn's end: never in the box, no hook yet
		rec := h.send(img("cccc3333_c.png") + " also this")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		typed, _ := h.term.counts()
		assert.Len(t, typed, 1, "typed once, not again")
	})
	t.Run("claude: a message to an agent in the middle of a turn, with a menu over its box: not confirmed", func(t *testing.T) {
		h := setup(t, "claude", true)
		h.event("tool_use")
		img := attach(t)
		h.term.show(ready(""))
		h.term.onType = func(string) { h.term.show(picker) } // no box to have queued it from: nothing says it was taken
		rec := h.send(img("eeee5555_e.png") + " and this")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_confirmed", code(rec))
	})
	t.Run("claude: a mid-turn agent whose box still holds the message (an image chip and the text): not confirmed (#4794, Security)", func(t *testing.T) {
		h := setup(t, "claude", true)
		h.event("tool_use")
		img := attach(t)
		h.term.show(ready(""))
		h.term.onType = func(string) { h.term.show(claude("[Image #1] and this")) } // typed, its Enter lost: still sitting in the box, not found as typed
		rec := h.send(img("ffff6666_f.png") + " and this")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_confirmed", code(rec))
	})
	t.Run("claude: an image and text an idle agent never takes: not confirmed", func(t *testing.T) {
		h := setup(t, "claude", true)
		img := attach(t)
		h.term.show(ready(""))
		rec := h.send(img("dddd4444_d.png") + " are you there")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "not_confirmed", code(rec))
	})
	t.Run("a path that isn't an upload is part of the message, and waits only the usual time", func(t *testing.T) {
		h := setup(t, "claude", true)
		attach(t)
		humanSendAttachConfirmWait = 5 * time.Second
		h.term.show(ready(""))
		start := time.Now()
		rec := h.send("/tmp/build.png is missing")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "not_confirmed", code(rec))
		assert.Less(t, time.Since(start), 2*time.Second, "not an attachment's wait")
	})
}

// #4794: a message has images attached when it starts with an upload's path (the composer puts them first).
func TestAttachesImages(t *testing.T) {
	old := uploadDir
	t.Cleanup(func() { uploadDir = old })
	uploadDir = "/home/a b/.coral/uploads"
	up := "/home/a b/.coral/uploads/"
	for _, c := range []struct {
		in   string
		want bool
	}{
		{up + "x_1.png hello", true},
		{up + "x_1.png " + up + "y_2.jpg two images", true},
		{up + "x_1.png", true},
		{"hello " + up + "x_1.png", false}, // not in front: the message's own words
		{"/tmp/x.png hello", false},
		{up + "sub/x.png hello", false}, // not an upload (uploads have no folders)
		{up + " hello", false},
		{"plain text", false},
	} {
		assert.Equal(t, c.want, attachesImages(c.in), c.in)
	}
	uploadDir = ""
	assert.False(t, attachesImages(up+"x_1.png hello"), "no uploads folder known")
}

// A slash command is one line: a slash, a name, then nothing or its arguments (#4705).
func TestSlashCommandRe(t *testing.T) {
	for text, want := range map[string]bool{
		"/compact": true, "/clear": true, "/model": true, "/model opus": true, "/review-pr 12": true, "/mcp:tool x": true,
		"/tmp/build.log is missing": false, "/": false, "/ compact": false, "//compact": false, "compact": false,
		" /compact": false, "/compact\nand more": false, "/model opus\nand more": false, "/9lives": false, "": false,
	} {
		assert.Equal(t, want, slashCommandRe.MatchString(text), "%q", text)
	}
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
