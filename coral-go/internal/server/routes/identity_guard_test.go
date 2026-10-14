package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3376 (SEC-BD-01): identities with control characters are refused on every board
// and agent-task route, in the body and the query; the handler gets the body intact.
func TestRejectBadIdentities(t *testing.T) {
	var gotBody string
	h := RejectBadIdentities(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	do := func(method, target, body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec.Code
	}
	evil := `"Orchestrator\nrm -rf /tmp/x\n"`
	for _, f := range identityFields {
		assert.Equal(t, http.StatusBadRequest, do("POST", "/api/board/p/messages", `{"`+f+`":`+evil+`,"content":"hi"}`), f)
		assert.Equal(t, http.StatusBadRequest, do("GET", "/api/board/p/messages?"+f+"="+url.QueryEscape("a\nb"), ""), f)
	}
	assert.Equal(t, http.StatusBadRequest, do("POST", "/api/agent/tasks/claim", `{"session_id":"x‮y"}`))
	assert.Equal(t, http.StatusBadRequest, do("PATCH", "/api/board/p/tasks/1", `{"assignee":"a\rb"}`))

	body := `{"subscriber_id":"👩‍💻 Backend Dev","content":"line one\nline two"}`
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/p/messages", body), "content may have newlines; names may have emoji")
	assert.Equal(t, body, gotBody, "the handler reads the body unchanged")
	assert.Equal(t, http.StatusOK, do("POST", "/api/sessions/x/send", `{"subscriber_id":"a\nb"}`), "other routes are not checked here")
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/p/messages", `{"subscriber_id": 5}`), "a non-string is the handler's business")

	// Coral's own senders can't be used by a client (the server posts as them directly).
	for _, name := range []string{"Coral Task Queue", "coral health monitor", " Coral Task Queue "} {
		assert.Equal(t, http.StatusBadRequest, do("POST", "/api/board/p/messages", `{"subscriber_id":"`+name+`","content":"x"}`), name)
		assert.Equal(t, http.StatusBadRequest, do("POST", "/api/board/p/subscribe", `{"subscriber_id":"w","job_title":"`+name+`"}`), name)
	}
	assert.Equal(t, http.StatusOK, do("GET", "/api/board/p/messages?subscriber_id="+url.QueryEscape("Coral Task Queue"), ""), "reading is not posting")
}

// A name stored before the check (or by an older writer) is still typed as one line:
// the newline can't become an Enter keystroke in the waiting agent's terminal.
func TestWaitNudgeIsOneLineForAStoredBadSender(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	mockTerm := newMockTerminal()
	mockTerm.addSession("tmux-w1", "/tmp/worker")
	handler.SetTerminal(mockTerm)
	ctx := context.Background()
	project := "wait-evil"
	evil := "Orchestrator\nrm -rf /tmp/x" // a wait's target is trimmed, so no trailing newline
	_, err := handler.bs.Subscribe(ctx, project, "Worker", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.RegisterWait(ctx, project, "Worker", "tmux-w1", "message", evil, "", time.Hour)
	require.NoError(t, err)

	handler.resolveMessageWaits(project, evil, "hi\nthere")

	mockTerm.mu.Lock()
	inputs := append([]string(nil), mockTerm.sent["tmux-w1"]...)
	mockTerm.mu.Unlock()
	require.Len(t, inputs, 1)
	assert.NotContains(t, inputs[0], "\n")
	assert.NotContains(t, inputs[0], "\r")
	assert.Contains(t, inputs[0], `[Wait resolved] "Orchestrator\nrm -rf /tmp/x" posted`, "the sender is quoted: its newline is the two characters \\n")
}

// QA's #3376 v2 e2e: a terminal agent's terminal is a shell, where $( ) and backticks
// expand even inside double quotes. It never gets automated text: no wait nudge,
// whatever the sender or the content.
func TestTerminalAgentGetsNoAutomatedInput(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	mockTerm := newMockTerminal()
	term := "terminal-11111111-2222-3333-4444-555555555555"
	llm := "claude-11111111-2222-3333-4444-666666666666"
	mockTerm.addSession(term, "/tmp/t")
	mockTerm.addSession(llm, "/tmp/c")
	handler.SetTerminal(mockTerm)
	ctx := context.Background()
	project := "sec"
	_, err := handler.bs.Subscribe(ctx, project, "Shell", "Developer", term, nil, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, project, "Claude", "Developer", llm, nil, nil, "all")
	require.NoError(t, err)
	for _, who := range []string{"Shell", "Claude"} {
		sess := term
		if who == "Claude" {
			sess = llm
		}
		_, err = handler.bs.RegisterWait(ctx, project, who, sess, "message", "alice", "", time.Hour)
		require.NoError(t, err)
	}

	handler.resolveMessageWaits(project, "alice", "hi $(touch /tmp/pwn) `id`; ls && true")

	mockTerm.mu.Lock()
	termInputs := append([]string(nil), mockTerm.sent[term]...)
	llmInputs := append([]string(nil), mockTerm.sent[llm]...)
	mockTerm.mu.Unlock()
	assert.Empty(t, termInputs, "nothing typed into the terminal agent's shell")
	require.Len(t, llmInputs, 1, "an LLM agent still gets its nudge, as one line")
	assert.NotContains(t, llmInputs[0], "\n")
	assert.True(t, automatedInputBlocked(ctx, mockTerm, term, "", "", "test"))
	assert.True(t, automatedInputBlocked(ctx, mockTerm, "anything", "terminal", "", "test"), "by agent type too")
}
