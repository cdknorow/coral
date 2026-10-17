package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
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
	mockTerm.addSession("claude-00000006-3864-4864-8864-000000000006", "/tmp/worker")
	handler.SetTerminal(mockTerm)
	ctx := context.Background()
	project := "wait-evil"
	evil := "Orchestrator\nrm -rf /tmp/x" // a wait's target is trimmed, so no trailing newline
	_, err := handler.bs.Subscribe(ctx, project, "Worker", "Developer", "claude-00000006-3864-4864-8864-000000000006", nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.RegisterWait(ctx, project, "Worker", "claude-00000006-3864-4864-8864-000000000006", "message", evil, "", time.Hour)
	require.NoError(t, err)

	handler.resolveMessageWaits(project, evil, "hi\nthere")

	mockTerm.mu.Lock()
	inputs := append([]string(nil), mockTerm.sent["claude-00000006-3864-4864-8864-000000000006"]...)
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
	_, err := handler.bs.Subscribe(ctx, project, "Shell", "Developer", term, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, project, "Claude", "Developer", llm, nil, "all")
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

// #4207: an agent's tool may not write as the Operator, in any spelling; the Operator's own callers
// may. The dashboard (no agent token, a browser) and the command line signed as the Operator hold a
// credential of their own, and what they write is theirs to spell: the signed command line sends the
// lower-case "operator" it is given (the first version refused that, on v1.4.72). Fields that only
// name someone (assignee, target) stay open to agents.
func TestOperatorNameIsOnlyTheOperators(t *testing.T) {
	h := RejectOperatorImpersonation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) }))
	do := func(headers map[string]string, verified bool, body string) int {
		req := httptest.NewRequest("POST", "/api/board/p/messages", strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if verified {
			req = cliauth.WithVerified(req) // what the identity layer does once it has checked the signature
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	dashboard := map[string]string{"User-Agent": "Mozilla/5.0", "Origin": "http://localhost:8420"}
	agentToken := map[string]string{"User-Agent": "coral-board/1.4", store.SessionTokenHeader: "tok"}
	agentCLI := map[string]string{"User-Agent": "coral-board/1.4"}
	signedCLI := map[string]string{"User-Agent": "coral-board/1.4", cliauth.CLIAuthHeader: "sig"}
	cyr := string(rune(0x41e)) + "perator" // a Cyrillic O

	// The dashboard and the signed command line write as the Operator in every field and spelling.
	for _, n := range []string{"Operator", "operator", "OPERATOR", " Operator ", "Operator" + string(rune(0xa0)), cyr} {
		for _, f := range []string{"subscriber_id", "as", "job_title", "from", "sender", "created_by"} {
			b, _ := json.Marshal(map[string]string{f: n, "content": "hi"})
			assert.Equal(t, http.StatusOK, do(dashboard, false, string(b)), "dashboard "+f+" "+n)
			assert.Equal(t, http.StatusOK, do(signedCLI, true, string(b)), "signed command line "+f+" "+n)
		}
	}
	// A signature header the server did not check (a forged one) does not make the agent command line the Operator.
	assert.Equal(t, http.StatusBadRequest, do(signedCLI, false, `{"subscriber_id":"operator"}`))
	// An agent's tool may not write as the Operator, with or without a token, in any spelling.
	for _, hd := range []map[string]string{agentToken, agentCLI} {
		for _, f := range []string{"subscriber_id", "as", "job_title", "from", "sender", "created_by"} {
			for _, n := range []string{"Operator", " operator ", "OPERATOR", cyr} {
				b, _ := json.Marshal(map[string]string{f: n, "content": "hi"})
				assert.Equal(t, http.StatusBadRequest, do(hd, false, string(b)), f+" "+n)
			}
		}
		// ...but may name the Operator as someone to address or assign to.
		assert.Equal(t, http.StatusOK, do(hd, false, `{"subscriber_id":"Dev","assignee":"Operator","target":"Operator"}`))
	}
	// Through Coral Hub the tunnel's own guard decides: its operator's spellings are not refused here.
	hub := map[string]string{"User-Agent": "Mozilla/5.0", tunnel.ViaHeader: "1"}
	for _, n := range []string{"Operator", "operator", "OPERATOR", " Operator ", "Operator" + string(rune(0xa0))} {
		b, _ := json.Marshal(map[string]string{"subscriber_id": n, "content": "x"})
		assert.Equal(t, http.StatusOK, do(hub, false, string(b)), n)
	}
}
