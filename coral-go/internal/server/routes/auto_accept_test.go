package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// Screens captured in a tmux pane (#3182): Claude Code 2.1.296 asking to run a
// Bash command, create a file, edit a file, and an AskUserQuestion; Codex
// 0.155.1 asking to run a command and to make edits (approvals_reviewer=user).
func autoAcceptScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "auto_accept", name+".txt"))
	require.NoError(t, err)
	return string(b)
}

// The approval table: only a recognised tool approval, only its approve-once
// option, for the agent type that draws it.
func TestClassifyApproval(t *testing.T) {
	alwaysOnly := ` Bash command
   rm -rf build
 Do you want to proceed?
 ❯ 1. Yes, and always allow access to build/ from this project
   2. No
 Esc to cancel · Tab to amend
`
	noNo := ` Do you want to proceed?
 ❯ 1. Yes
   2. Maybe later
`
	reordered := ` Bash command
   npm test
 Do you want to proceed?
 ❯ 1. Yes, and don't ask again for npm test commands in this project
   2. Yes
   3. No
`
	codexReordered := `  Would you like to run the following command?
  $ npm test
› 1. Yes, and don't ask again for commands that start with ` + "`npm test`" + ` (p)
  2. Yes, proceed (y)
  3. No, and tell Codex what to do differently (esc)
  Press enter to confirm or esc to cancel
`
	agyReordered := `Requesting permission for:
   printf x; exit 1

Run this command?
  1. Yes, and always allow in this conversation for commands that start with 'printf'
> 2. Yes, run command
  3. Yes, and always allow for commands that start with 'printf' (Persist to settings.json)
  4. No, cancel

  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
`
	agyAlwaysOnly := `Requesting permission for:
   printf x; exit 1

Run this command?
> 1. Yes, and always allow in this conversation for commands that start with 'printf'
  2. Yes, and always allow for commands that start with 'printf' (Persist to settings.json)
  3. No, cancel

  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
`
	agyLogin := `
 Welcome to the Antigravity CLI. You are currently not signed in.

 Select login method:
 > 1. Google OAuth
   2. Use a Google Cloud project

   ↑/↓ Navigate · enter Select
`
	for _, tc := range []struct {
		name, agentType, screen string
		prompt, approve         bool
		n                       int
		label, kind             string
	}{
		{"claude bash", at.Claude, autoAcceptScreen(t, "claude_bash"), true, true, 1, "Yes", ""},
		{"claude write", at.Claude, autoAcceptScreen(t, "claude_write"), true, true, 1, "Yes", ""},
		{"claude edit", at.Claude, autoAcceptScreen(t, "claude_edit"), true, true, 1, "Yes", ""},
		{"claude 2.1.278 bash", at.Claude, screenPermission, true, true, 1, "Yes", ""},
		{"claude: approve-once wherever it is listed", at.Claude, reordered, true, true, 2, "Yes", ""},
		{"claude question", at.Claude, autoAcceptScreen(t, "claude_question"), true, false, 0, "", ""},
		{"claude question (2.1.278)", at.Claude, screenQuestion, true, false, 0, "", ""},
		{"claude plan approval", at.Claude, screenPlan, true, false, 0, "", ""},
		{"claude review step", at.Claude, screenReview, true, false, 0, "", ""},
		{"claude: only an always option", at.Claude, alwaysOnly, true, false, 0, "", ""},
		{"claude: no No option is not an approval", at.Claude, noNo, true, false, 0, "", ""},
		{"claude idle list", at.Claude, screenIdleList, false, false, 0, "", ""},
		{"claude trust prompt (no digits)", at.Claude, screenTrust, false, false, 0, "", ""},
		{"codex command", at.Codex, autoAcceptScreen(t, "codex_exec"), true, true, 1, "Yes, proceed (y)", "command"},
		{"codex edits", at.Codex, autoAcceptScreen(t, "codex_edit"), true, true, 1, "Yes, proceed (y)", "edit"},
		{"codex: approve-once wherever it is listed", at.Codex, codexReordered, true, true, 2, "Yes, proceed (y)", "command"},
		{"codex sign-in menu", at.Codex, screenCodexSignIn, true, false, 0, "", ""},
		{"codex update menu", at.Codex, screenCodexUpdate, true, false, 0, "", ""},
		{"agy command", at.Agy, autoAcceptScreen(t, "agy_command"), true, true, 1, "Yes, run command", "command"},
		{"gemini command", at.Gemini, autoAcceptScreen(t, "agy_command"), true, true, 1, "Yes, run command", "command"},
		{"agy: approve-once wherever it is listed", at.Agy, agyReordered, true, true, 2, "Yes, run command", "command"},
		{"agy: only always allow options", at.Agy, agyAlwaysOnly, true, false, 0, "", ""},
		{"agy login menu", at.Agy, agyLogin, true, false, 0, "", ""},
		{"a claude prompt is not a codex approval", at.Codex, autoAcceptScreen(t, "claude_bash"), true, false, 0, "", ""},
		{"a codex prompt is not a claude approval", at.Claude, autoAcceptScreen(t, "codex_exec"), true, false, 0, "", ""},
		{"a claude prompt is not a gemini approval", at.Gemini, autoAcceptScreen(t, "claude_bash"), true, false, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := classifyApproval(tc.agentType, tc.screen)
			assert.Equal(t, tc.prompt, d.Prompt, "prompt")
			assert.Equal(t, tc.approve, d.Approve, "approve")
			assert.Equal(t, tc.n, d.N)
			assert.Equal(t, tc.label, d.Label)
			if tc.kind != "" {
				assert.Equal(t, tc.kind, d.Kind)
			}
			if tc.prompt {
				assert.NotEmpty(t, d.FP)
			}
		})
	}

	// A different command or file is a different prompt.
	bash := autoAcceptScreen(t, "claude_bash")
	a := classifyApproval(at.Claude, bash)
	b := classifyApproval(at.Claude, strings.Replace(bash, "\n touch probe.txt\n", "\n rm -rf probe\n", 1))
	require.True(t, b.Approve)
	assert.NotEqual(t, a.FP, b.FP)
	assert.Equal(t, a.FP, classifyApproval(at.Claude, bash).FP, "stable for the same screen")
}

func TestAutoAcceptSupported(t *testing.T) {
	for _, typ := range []string{at.Claude, at.Codex, at.Agy, at.Antigravity, at.Gemini} {
		assert.True(t, AutoAcceptSupported(typ), typ)
	}
	for _, typ := range []string{at.Pi, at.Terminal, "", "zz"} {
		assert.False(t, AutoAcceptSupported(typ), typ)
	}
}

type autoAcceptRig struct {
	t      *testing.T
	server *httptest.Server
	h      *SessionsHandler
	term   *mockSessionTerminal
	a      *AutoAcceptor
	now    time.Time
	notifs *NotificationStore
	n      int
}

func newAutoAcceptRig(t *testing.T) *autoAcceptRig {
	server, h, term, _ := setupSessionsTestServer(t)
	rig := &autoAcceptRig{t: t, server: server, h: h, term: term, now: time.Now().UTC().Truncate(time.Second)}
	rig.notifs = NewNotificationStore()
	h.SetNotificationStore(rig.notifs)
	rig.a = NewAutoAcceptor(h)
	rig.a.now = func() time.Time { return rig.now }
	// The owner has let the Coral Hub turn auto-accept on (off unless turned on, #126 F4).
	require.NoError(t, h.ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "true"))
	return rig
}

// agent registers a live session (working dir dir) with its own pane showing screen.
func (r *autoAcceptRig) agentIn(agentType, name, dir, screen string) string {
	r.n++
	sid := fmt.Sprintf("%08d-2222-3333-4444-555555555555", r.n)
	require.NoError(r.t, r.h.ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: name, WorkingDir: dir}))
	pane := naming.SessionName(agentType, sid)
	r.term.addSession(pane, dir)
	r.screen(agentType, sid, screen)
	return sid
}

func (r *autoAcceptRig) agent(agentType, name, screen string) string {
	return r.agentIn(agentType, name, "/tmp/"+name, screen)
}

func (r *autoAcceptRig) screen(agentType, sid, screen string) {
	r.term.setOutput(naming.SessionName(agentType, sid), screen)
}

func (r *autoAcceptRig) keys(agentType, sid string) []string {
	r.term.mu.Lock()
	defer r.term.mu.Unlock()
	return append([]string(nil), r.term.raw[naming.SessionName(agentType, sid)]...)
}

func (r *autoAcceptRig) enters(agentType, sid string) int {
	r.term.mu.Lock()
	defer r.term.mu.Unlock()
	return r.term.enters[naming.SessionName(agentType, sid)]
}

// The dashboard's fetch: same-origin, from this computer.
var dashboardHdr = map[string]string{"Sec-Fetch-Site": "same-origin"}

func (r *autoAcceptRig) post(path string, body any, hdr map[string]string) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, r.server.URL+path, bytes.NewReader(b))
	require.NoError(r.t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (r *autoAcceptRig) enableAs(name, sid string, hdr map[string]string, extra map[string]any) (int, map[string]any) {
	body := map[string]any{"session_id": sid, "acknowledged": true}
	for k, v := range extra {
		body[k] = v
	}
	return r.post("/api/sessions/live/"+name+"/auto-accept/enable", body, hdr)
}

func (r *autoAcceptRig) enable(name, sid string) {
	code, body := r.enableAs(name, sid, dashboardHdr, nil)
	require.Equal(r.t, http.StatusOK, code, body)
}

func (r *autoAcceptRig) notify(name, sid, summary string) {
	_, err := r.h.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "notification", Summary: summary})
	require.NoError(r.t, err)
}

func (r *autoAcceptRig) events(sid, eventType string) []store.AgentEvent {
	all, err := r.h.ts.ListAgentEvents(context.Background(), "", 200, &sid)
	require.NoError(r.t, err)
	var out []store.AgentEvent
	for _, e := range all {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

func (r *autoAcceptRig) state(sid string) store.AutoAcceptState {
	st, err := r.h.ss.GetAutoAcceptState(context.Background(), sid)
	require.NoError(r.t, err)
	return st
}

func (r *autoAcceptRig) run() { r.a.RunOnce(context.Background()) }

func detailOf(t *testing.T, e store.AgentEvent) map[string]any {
	t.Helper()
	require.NotNil(t, e.DetailJSON)
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(*e.DetailJSON), &d))
	return d
}

// Claude: a permission prompt is answered with its approve-once digit only
// while auto-accept is on and Claude reported the request; the approval is
// recorded and ends the needs-input state.
func TestAutoAcceptClaudePermissionPrompt(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Claude, "dev", autoAcceptScreen(t, "claude_bash"))

	r.notify("dev", sid, "Notification: Claude needs your permission to use Bash")
	r.run()
	assert.Empty(t, r.keys(at.Claude, sid), "off: nothing is answered")

	r.enable("dev", sid)
	r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "touch probe.txt"}, At: r.now})
	r.run()
	assert.Equal(t, []string{"1"}, r.keys(at.Claude, sid), "the approve-once option")
	ev := r.events(sid, "auto_accept")
	require.Len(t, ev, 1)
	assert.Equal(t, "Auto-approved Bash: touch probe.txt", ev[0].Summary)
	assert.Equal(t, map[string]any{"agent_type": "claude", "option": "Yes", "tool": "Bash", "input": "touch probe.txt"}, detailOf(t, ev[0]))
	assert.False(t, r.h.deriveSingleSessionState(context.Background(), sid, 0).NeedsInput, "the auto_accept event ends the request")

	// The next run finds no request: nothing more is sent.
	r.now = r.now.Add(10 * time.Second)
	r.run()
	assert.Equal(t, []string{"1"}, r.keys(at.Claude, sid))

	// Without a permission request a prompt-looking screen is left alone.
	r.screen(at.Claude, sid, autoAcceptScreen(t, "claude_edit"))
	r.run()
	assert.Equal(t, []string{"1"}, r.keys(at.Claude, sid), "no request, no answer")

	// The pending tool (PreToolUse) names what was approved.
	r.h.pending.set(sid, pendingTool{ToolName: "Edit", Input: map[string]any{"file_path": "notes.txt"}, At: r.now})
	r.notify("dev", sid, "Notification: Claude needs your permission to use Edit")
	r.run()
	assert.Equal(t, []string{"1", "1"}, r.keys(at.Claude, sid))
	ev = r.events(sid, "auto_accept")
	require.Len(t, ev, 2)
	assert.Equal(t, "Auto-approved Edit: notes.txt", ev[0].Summary, "newest first")

	// The input that reaches the browser is capped at 200 characters.
	long := "echo " + strings.Repeat("x", 400)
	r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": long}, At: r.now})
	r.screen(at.Claude, sid, autoAcceptScreen(t, "claude_bash"))
	r.notify("dev", sid, "Notification: Claude needs your permission to use Bash")
	r.run()
	ev = r.events(sid, "auto_accept")
	require.Len(t, ev, 3)
	in := detailOf(t, ev[0])["input"].(string)
	assert.LessOrEqual(t, len([]rune(in)), autoAcceptInputMaxLen+1)
}

// agy: a permission prompt is auto-accepted with Enter only while auto-accept is on
// and agy has a pending tool; it records the auto_accept event.
func TestAutoAcceptAgyPermissionPrompt(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Agy, "agy-dev", autoAcceptScreen(t, "agy_command"))

	r.run()
	assert.Equal(t, 0, r.enters(at.Agy, sid), "off: nothing is answered")

	r.enable("agy-dev", sid)
	// Without pending tool, agy is not auto-accepted (rule: pending tool required)
	r.run()
	assert.Equal(t, 0, r.enters(at.Agy, sid), "no pending tool: nothing is answered")

	r.h.pending.set(sid, pendingTool{ToolName: "run_command", Input: map[string]any{"CommandLine": "touch probe.txt"}, At: r.now})
	r.run()
	assert.Equal(t, 1, r.enters(at.Agy, sid), "the approve-once option is confirmed with Enter")
	ev := r.events(sid, "auto_accept")
	require.Len(t, ev, 1)
	assert.Equal(t, "Auto-approved run_command: touch probe.txt", ev[0].Summary)
	assert.Equal(t, map[string]any{"agent_type": "agy", "option": "Yes, run command", "tool": "run_command", "input": "touch probe.txt"}, detailOf(t, ev[0]))
	assert.False(t, r.h.deriveSingleSessionState(context.Background(), sid, 0).NeedsInput, "the auto_accept event ends the request")
}

// Questions, plan approval, high-risk actions and unknown prompts wait for the
// user; each is recorded as skipped.
func TestAutoAcceptNeverAnswersQuestionsPlansOrRisks(t *testing.T) {
	for _, tc := range []struct {
		name, tool, screen, summary string
		input                       map[string]any
	}{
		{"question", "AskUserQuestion", autoAcceptScreen(t, "claude_question"), "Not auto-accepted: a question waits for your answer", nil},
		{"plan", "ExitPlanMode", screenPlan, "Not auto-accepted: plan approval waits for you", nil},
		// The approval screen itself is never answered for a question tool.
		{"question tool with a yes-no screen", "AskUserQuestion", autoAcceptScreen(t, "claude_bash"), "Not auto-accepted: a question waits for your answer", nil},
		{"unknown prompt", "Bash", "Pick a mode\n❯ 1. Fast\n  2. Careful\n", "Not auto-accepted: unrecognised prompt", nil},
		{"shell write outside", "Bash", autoAcceptScreen(t, "claude_bash"), "Not auto-accepted (high-risk): it writes outside the working directory (~/.zshrc)", map[string]any{"command": "echo x >> ~/.zshrc"}},
		{"force push", "Bash", autoAcceptScreen(t, "claude_bash"), "Not auto-accepted (high-risk): it force-pushes", map[string]any{"command": "git push --force origin main"}},
		{"write outside", "Write", autoAcceptScreen(t, "claude_write"), "Not auto-accepted (high-risk): it writes a file outside the working directory (/etc/hosts)", map[string]any{"file_path": "/etc/hosts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoAcceptRig(t)
			sid := r.agent(at.Claude, "dev", tc.screen)
			r.enable("dev", sid)
			r.h.pending.set(sid, pendingTool{ToolName: tc.tool, Input: tc.input, At: r.now})
			r.notify("dev", sid, "Notification: Claude needs your permission to use "+tc.tool)
			for i := 0; i < 3; i++ {
				r.run()
				r.now = r.now.Add(5 * time.Second)
			}
			assert.Empty(t, r.keys(at.Claude, sid), "never answered")
			skipped := r.events(sid, "auto_accept_skipped")
			require.Len(t, skipped, 1, "recorded once")
			assert.Equal(t, tc.summary, skipped[0].Summary)
		})
	}
}

// A risky command seen only on screen (no PreToolUse record) is caught too.
func TestAutoAcceptRiskFromTheScreen(t *testing.T) {
	r := newAutoAcceptRig(t)
	exec := autoAcceptScreen(t, "codex_exec")
	sid := r.agent(at.Codex, "cx", strings.Replace(exec, "  $ touch\n", "  $ sudo rm -rf\n", 1))
	r.enable("cx", sid)
	r.run()
	assert.Empty(t, r.keys(at.Codex, sid))
	skipped := r.events(sid, "auto_accept_skipped")
	require.Len(t, skipped, 1)
	assert.Equal(t, "Not auto-accepted (high-risk): it uses sudo", skipped[0].Summary)

	// A Codex edit outside the working directory waits too; inside, it is answered.
	out := r.agent(at.Codex, "cx2", autoAcceptScreen(t, "codex_edit"))
	r.enable("cx2", out)
	in := r.agentIn(at.Codex, "cx3", "/private/tmp/claude-501", autoAcceptScreen(t, "codex_edit"))
	r.enable("cx3", in)
	r.run()
	assert.Empty(t, r.keys(at.Codex, out))
	assert.Equal(t, []string{"1"}, r.keys(at.Codex, in))
}

// A screen flapping between prompts records each once and at most one skipped
// event per session per minute.
func TestAutoAcceptSkippedEventsAreCapped(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Codex, "cx", "")
	r.enable("cx", sid)
	for i := 0; i < 10; i++ {
		r.screen(at.Codex, sid, fmt.Sprintf("Pick %d\n› 1. A%d\n  2. B\n", i%2, i%2))
		r.run()
		r.now = r.now.Add(5 * time.Second)
	}
	assert.Len(t, r.events(sid, "auto_accept_skipped"), 1, "50 s of flapping: one event")
	r.now = r.now.Add(time.Minute)
	r.screen(at.Codex, sid, "Pick 1\n› 1. A1\n  2. B\n")
	r.run()
	assert.Len(t, r.events(sid, "auto_accept_skipped"), 2, "the other prompt is recorded once the minute is up")
	r.now = r.now.Add(2 * time.Minute)
	r.run()
	r.screen(at.Codex, sid, "Pick 0\n› 1. A0\n  2. B\n")
	r.run()
	assert.Len(t, r.events(sid, "auto_accept_skipped"), 2, "each prompt only once")
	assert.Empty(t, r.keys(at.Codex, sid))
}

// Codex has no permission hook: its screen is read on each sweep. A prompt is
// answered once (debounce); the same prompt approved five times in a minute
// pauses auto-accept with a code, an event and a notification.
func TestAutoAcceptCodexDebounceAndLoopGuard(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Codex, "cx", autoAcceptScreen(t, "codex_exec"))
	r.enable("cx", sid)

	r.run()
	r.run() // the screen has not caught up yet
	assert.Equal(t, []string{"1"}, r.keys(at.Codex, sid), "answered once")
	ev := r.events(sid, "auto_accept")
	require.Len(t, ev, 1)
	assert.True(t, strings.HasPrefix(ev[0].Summary, "Auto-approved command: touch /private/tmp/"), ev[0].Summary)

	for i := 0; i < 4; i++ {
		r.now = r.now.Add(4 * time.Second)
		r.run()
	}
	assert.Len(t, r.keys(at.Codex, sid), 5)
	r.now = r.now.Add(4 * time.Second)
	r.run()
	assert.Len(t, r.keys(at.Codex, sid), 5, "the sixth is not answered")
	st := r.state(sid)
	assert.False(t, st.Enabled, "paused")
	assert.Equal(t, "repeat_loop", st.PausedCode.String)
	paused := r.events(sid, "auto_accept_paused")
	require.Len(t, paused, 1)
	assert.Equal(t, "repeat_loop", detailOf(t, paused[0])["code"])
	backlog, _ := r.notifs.Connect()
	require.Len(t, backlog, 1)
	assert.Equal(t, "warning", backlog[0].Level)

	r.now = r.now.Add(time.Minute)
	r.run()
	assert.Len(t, r.keys(at.Codex, sid), 5, "off: nothing more is answered")
	r.enable("cx", sid)
	assert.False(t, r.state(sid).PausedCode.Valid, "enabling again clears the code")
}

// More than 30 approvals in a minute, all different, pause it too.
func TestAutoAcceptTotalRateCap(t *testing.T) {
	r := newAutoAcceptRig(t)
	exec := autoAcceptScreen(t, "codex_exec")
	sid := r.agent(at.Codex, "cx", exec)
	r.enable("cx", sid)
	for i := 0; i < autoAcceptTotalLimit+1; i++ {
		r.screen(at.Codex, sid, strings.Replace(exec, "  $ touch\n", fmt.Sprintf("  $ touch f%d\n", i), 1))
		r.run()
		r.now = r.now.Add(time.Second)
	}
	assert.Len(t, r.keys(at.Codex, sid), autoAcceptTotalLimit)
	st := r.state(sid)
	assert.False(t, st.Enabled)
	assert.Equal(t, "rate_cap", st.PausedCode.String)
}

// The digit is sent only if the re-read screen still shows the same prompt,
// and only to the session's own pane.
func TestAutoAcceptRecapturesAndChecksThePane(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Codex, "cx", autoAcceptScreen(t, "codex_exec"))
	r.enable("cx", sid)
	pane := naming.SessionName(at.Codex, sid)
	r.term.onCapture = func(name string) {
		if name == pane {
			r.term.setOutput(pane, autoAcceptScreen(t, "claude_bash")) // a different prompt by the time we answer
		}
	}
	r.run()
	assert.Empty(t, r.keys(at.Codex, sid), "the prompt changed: nothing sent")
	r.term.onCapture = nil

	// The pane resolves to another session (its own is gone): nothing is read or sent.
	other := r.agent(at.Codex, "cx2", autoAcceptScreen(t, "codex_exec"))
	r.enable("cx2", other)
	r.term.mu.Lock()
	r.term.sessions[naming.SessionName(at.Codex, other)].SessionName = "codex-someone-else"
	r.term.captures = nil
	r.term.mu.Unlock()
	r.run()
	assert.Empty(t, r.keys(at.Codex, other))
	r.term.mu.Lock()
	assert.NotContains(t, r.term.captures, naming.SessionName(at.Codex, other), "another session's screen is not read")
	r.term.mu.Unlock()
}

// Who may turn it on: the dashboardHdr, or the Cloud Hub through this process's
// connector (the stamp is a per-process secret). Never an agent, never a
// client without browser provenance, never a forged stamp. Turning it off is
// always allowed.
func TestAutoAcceptEnableProvenance(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Claude, "dev", "")
	refused := func(hdr map[string]string, wantCode string) {
		t.Helper()
		code, body := r.enableAs("dev", sid, hdr, nil)
		assert.Equal(t, http.StatusForbidden, code, "%v: %v", hdr, body)
		assert.Equal(t, wantCode, body["code"], "%v", hdr)
		assert.False(t, r.state(sid).Enabled)
	}
	refused(nil, "no_dashboard_provenance")                                                                       // curl, coral-agent, coral-board
	refused(map[string]string{"Sec-Fetch-Site": "cross-site"}, "no_dashboard_provenance")                         // another web page
	refused(map[string]string{tunnel.ViaHeader: "1"}, "no_dashboard_provenance")                                  // a forged stamp
	refused(map[string]string{tunnel.ViaHeader: "deadbeef"}, "no_dashboard_provenance")                           // a guessed one
	refused(map[string]string{tunnel.ViaHeader: "1", "Sec-Fetch-Site": "same-origin"}, "no_dashboard_provenance") // a stamp never relaxes
	refused(map[string]string{"Sec-Fetch-Site": "same-origin", "Authorization": "Bearer k"}, "no_dashboard_provenance")
	for _, hd := range agentIdentityHeaders {
		refused(map[string]string{"Sec-Fetch-Site": "same-origin", hd: "dev"}, "agent_identity")
	}

	// The dashboard.
	code, body := r.enableAs("dev", sid, dashboardHdr, nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, true, body["auto_accept"])
	assert.Equal(t, "local", body["auto_accept_source"])
	assert.Nil(t, body["auto_accept_until"], "a local enable has no time limit unless asked")
	en := r.events(sid, "auto_accept_enabled")
	require.Len(t, en, 1)
	assert.Equal(t, "Auto-accept turned on from the dashboard", en[0].Summary)

	// Off from anywhere, even curl.
	code, body = r.post("/api/sessions/live/dev/auto-accept/disable", map[string]any{"session_id": sid}, nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, false, body["auto_accept"])
	assert.Nil(t, body["auto_accept_paused_code"])
	require.Len(t, r.events(sid, "auto_accept_disabled"), 1)

	// The Cloud Hub through the connector, while the owner's switch is unset: off by default (#126 F4).
	_, err := r.h.ss.DeleteSetting(context.Background(), AllowHubAutoAcceptSetting)
	require.NoError(t, err)
	refused(map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, "hub_auto_accept_disabled")
	require.NoError(t, r.h.ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "true"))
	// With it on: a hub enable, an hour by default.
	code, body = r.enableAs(sid, sid, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "hub", body["auto_accept_source"])
	assert.Equal(t, float64(r.now.Add(time.Hour).Unix()), body["auto_accept_until"])
	assert.Equal(t, "Auto-accept turned on from the Coral Hub", r.events(sid, "auto_accept_enabled")[0].Summary)
	code, body = r.enableAs(sid, sid, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, map[string]any{"duration_s": 600})
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, float64(r.now.Add(10*time.Minute).Unix()), body["auto_accept_until"], "the hub may ask for less")

	// The owner's switch off: the hub may not, the dashboard still may.
	require.NoError(t, r.h.ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "false"))
	code, body = r.enableAs(sid, sid, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_auto_accept_disabled", body["code"])
	code, _ = r.enableAs("dev", sid, dashboardHdr, nil)
	assert.Equal(t, http.StatusOK, code)
}

// The enable request: acknowledged, duration bounds, unsupported, asleep, unknown.
func TestAutoAcceptEnableValidation(t *testing.T) {
	r := newAutoAcceptRig(t)
	ctx := context.Background()
	sid := r.agent(at.Claude, "dev", "")

	code, body := r.post("/api/sessions/live/dev/auto-accept/enable", map[string]any{"session_id": sid}, dashboardHdr)
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body["error"], "acknowledged")
	code, _ = r.post("/api/sessions/live/dev/auto-accept/enable", map[string]any{"session_id": sid, "acknowledged": false}, dashboardHdr)
	assert.Equal(t, http.StatusBadRequest, code)
	for _, d := range []int{0, -5, 28801} {
		code, _ = r.enableAs("dev", sid, dashboardHdr, map[string]any{"duration_s": d})
		assert.Equal(t, http.StatusBadRequest, code, d)
	}
	code, body = r.enableAs("dev", sid, dashboardHdr, map[string]any{"duration_s": 28800})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, float64(r.now.Add(8*time.Hour).Unix()), body["auto_accept_until"], "a local enable may set a limit")

	code, _ = r.enableAs("dev", "99999999-2222-3333-4444-555555555555", dashboardHdr, nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = r.enableAs("someone-else", sid, dashboardHdr, nil)
	assert.Equal(t, http.StatusNotFound, code, "the path must name the session")
	code, _ = r.post("/api/sessions/live/dev/auto-accept/disable", map[string]any{"session_id": "99999999-2222-3333-4444-555555555555"}, nil)
	assert.Equal(t, http.StatusNotFound, code)

	term := r.agent(at.Terminal, "term", "")
	code, body = r.enableAs("term", term, dashboardHdr, nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "agent_type_unsupported", body["code"])

	require.NoError(t, r.h.ss.SetSessionSleeping(ctx, sid, true))
	code, body = r.enableAs("dev", sid, dashboardHdr, nil)
	assert.Equal(t, http.StatusConflict, code, "an asleep agent cannot be enabled: %v", body)
}

// Expiry, Stop all, and the Cloud Hub's enables ending with Coral.
func TestAutoAcceptExpiryStopAllAndRestart(t *testing.T) {
	r := newAutoAcceptRig(t)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}
	a := r.agent(at.Claude, "a", "")
	b := r.agent(at.Codex, "b", "")
	c := r.agent(at.Claude, "c", "")
	code, _ := r.enableAs(a, a, hub, map[string]any{"duration_s": 60})
	require.Equal(t, http.StatusOK, code)
	r.enable("b", b)
	code, _ = r.enableAs(c, c, hub, nil)
	require.Equal(t, http.StatusOK, code)

	r.now = r.now.Add(61 * time.Second)
	r.run()
	assert.False(t, r.state(a).Enabled, "expired")
	assert.Equal(t, "expired", r.state(a).PausedCode.String)
	cl := r.events(a, "auto_accept_cleared")
	require.Len(t, cl, 1)
	assert.Equal(t, "expired", detailOf(t, cl[0])["code"])
	assert.True(t, r.state(b).Enabled)
	assert.True(t, r.state(c).Enabled)

	// A new Coral process ends the hub's enables, not the local ones.
	NewAutoAcceptor(r.h).Start(context.Background())
	assert.True(t, r.state(b).Enabled, "a local enable survives a Coral restart")
	assert.False(t, r.state(c).Enabled)
	assert.Equal(t, "coral_restarted", r.state(c).PausedCode.String)

	// Stop all.
	r.enable("a", a)
	code, body := r.post("/api/auto-accept/disable-all", map[string]any{}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, float64(2), body["stopped"])
	for _, sid := range []string{a, b} {
		st := r.state(sid)
		assert.False(t, st.Enabled)
		assert.Equal(t, "stopped_by_owner", st.PausedCode.String)
		ev := r.events(sid, "auto_accept_cleared")
		require.NotEmpty(t, ev)
		assert.Equal(t, "stopped_by_owner", detailOf(t, ev[0])["code"])
	}
	code, body = r.post("/api/auto-accept/disable-all", map[string]any{}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, float64(0), body["stopped"])
}

// A new agent run starts without auto-accept, with an auto_accept_cleared
// event saying why: sleep, wake, archive, restart/resume, the run ending.
func TestAutoAcceptClearedOnANewRun(t *testing.T) {
	r := newAutoAcceptRig(t)
	ctx := context.Background()
	for _, tc := range []struct {
		code  string
		clear func(sid string)
	}{
		{"agent_slept", func(sid string) { require.NoError(t, r.h.ss.SetSessionSleeping(ctx, sid, true)) }},
		{"agent_archived", func(sid string) { require.NoError(t, r.h.ss.SetSessionArchived(ctx, sid, true)) }},
		{"run_ended", func(sid string) { require.NoError(t, r.h.ss.UnregisterLiveSession(ctx, sid)) }},
		{"agent_restarted", func(sid string) {
			require.NoError(t, r.h.ss.ReplaceLiveSession(ctx, sid, &store.LiveSession{SessionID: "77777777-2222-3333-4444-555555555555", AgentType: at.Claude, AgentName: "x", WorkingDir: "/tmp"}))
			assert.False(t, r.state("77777777-2222-3333-4444-555555555555").Enabled, "the new run starts off")
		}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			sid := r.agent(at.Claude, "x"+tc.code, "")
			r.enable("x"+tc.code, sid)
			tc.clear(sid)
			st := r.state(sid)
			assert.False(t, st.Enabled)
			assert.False(t, st.PausedCode.Valid, "a new run is not 'paused'")
			ev := r.events(sid, "auto_accept_cleared")
			require.Len(t, ev, 1)
			assert.Equal(t, tc.code, detailOf(t, ev[0])["code"])
		})
	}
	// Waking clears what the startup recovery woke (and logs it, if it was on).
	sid := r.agent(at.Claude, "w", "")
	_, err := r.h.ss.EnableAutoAccept(ctx, sid, store.AutoAcceptSourceLocal, 0)
	require.NoError(t, err)
	_, err = r.h.ss.DB().ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = 1 WHERE session_id = ?", sid)
	require.NoError(t, err)
	require.NoError(t, r.h.ss.SetSessionSleeping(ctx, sid, false))
	assert.False(t, r.state(sid).Enabled)
	require.Len(t, r.events(sid, "auto_accept_cleared"), 1)
	assert.Equal(t, "agent_woken", detailOf(t, r.events(sid, "auto_accept_cleared")[0])["code"])
	// Turning it off yourself is not 'cleared'.
	sid = r.agent(at.Claude, "y", "")
	r.enable("y", sid)
	code, _ := r.post("/api/sessions/live/y/auto-accept/disable", map[string]any{"session_id": sid}, dashboardHdr)
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, r.events(sid, "auto_accept_cleared"))
	// It survives a Coral restart while the run lives: the row is what the engine reads.
	sid = r.agent(at.Claude, "z", "")
	r.enable("z", sid)
	NewAutoAcceptor(r.h).Start(ctx)
	rows, err := r.h.ss.ListAutoAcceptSessions(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sid, rows[0].SessionID)
}

// Every list, single-session and feed entry carries all seven fields, false and
// null included (clients merge entries, so an omitted false would keep the AUTO
// chip on), and codes only, never prompt text.
func TestAutoAcceptFieldsAlwaysPresent(t *testing.T) {
	h, awake := sessionFieldsFixture(t)
	ctx := context.Background()
	asleep := "22222222-2222-3333-4444-555555555555"
	require.NoError(t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: asleep, AgentType: at.Terminal, AgentName: "term", WorkingDir: "/tmp/term"}))
	require.NoError(t, h.ss.SetSessionSleeping(ctx, asleep, true))
	keys := []string{"auto_accept", "auto_accept_until", "auto_accept_source", "auto_accept_supported", "auto_accept_unsupported_code", "auto_accept_paused_code", "auto_accept_paused_at"}

	check := func(phase string, want map[string]map[string]any) {
		w := httptest.NewRecorder()
		h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
		require.Equal(t, 200, w.Code)
		var list []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		ws, err := h.buildSessionListForWS(httptest.NewRequest("GET", "/ws/coral", nil))
		require.NoError(t, err)
		b, _ := json.Marshal(ws)
		var wsDecoded []map[string]any
		require.NoError(t, json.Unmarshal(b, &wsDecoded))
		for src, entries := range map[string][]map[string]any{"list": list, "ws": wsDecoded} {
			seen := 0
			for _, e := range entries {
				sid, _ := e["session_id"].(string)
				exp, ok := want[sid]
				if !ok {
					continue
				}
				seen++
				for _, k := range keys {
					v, present := e[k]
					require.True(t, present, "%s %s %s: %s is present", phase, src, sid, k)
					assert.Equal(t, exp[k], v, "%s %s %s: %s", phase, src, sid, k)
				}
			}
			assert.Equal(t, len(want), seen, "%s %s", phase, src)
		}
		w = httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/sessions/"+awake+"/resolve", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionID", awake)
		h.ResolveSession(w, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
		require.Equal(t, 200, w.Code)
		var single map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &single))
		for _, k := range keys {
			v, present := single[k]
			require.True(t, present, "%s resolve: %s is present", phase, k)
			assert.Equal(t, want[awake][k], v, "%s resolve: %s", phase, k)
		}
	}
	fields := func(on bool, until, source, paused any, pausedAt ...any) map[string]any {
		var at any
		if len(pausedAt) > 0 {
			at = pausedAt[0]
		}
		return map[string]any{"auto_accept": on, "auto_accept_until": until, "auto_accept_source": source, "auto_accept_supported": true,
			"auto_accept_unsupported_code": nil, "auto_accept_paused_code": paused, "auto_accept_paused_at": at}
	}
	gem := map[string]any{"auto_accept": false, "auto_accept_until": nil, "auto_accept_source": nil, "auto_accept_supported": false,
		"auto_accept_unsupported_code": "agent_type_unsupported", "auto_accept_paused_code": nil, "auto_accept_paused_at": nil}
	check("never used", map[string]map[string]any{awake: fields(false, nil, nil, nil), asleep: gem})

	_, err := h.ss.EnableAutoAccept(ctx, awake, store.AutoAcceptSourceHub, 1_900_000_000)
	require.NoError(t, err)
	check("on from the hub", map[string]map[string]any{awake: fields(true, float64(1_900_000_000), "hub", nil), asleep: gem})

	before := time.Now().Unix()
	require.NoError(t, h.ss.PauseAutoAccept(ctx, awake, store.AutoAcceptRepeatLoop))
	st, err := h.ss.GetAutoAcceptState(ctx, awake)
	require.NoError(t, err)
	require.True(t, st.PausedAt.Valid && st.PausedAt.Int64 >= before && st.PausedAt.Int64 <= time.Now().Unix(), "paused_at is when it paused")
	// When it paused rides with the code (#4521): a client shows each pause once, not on every reload.
	check("paused", map[string]map[string]any{awake: fields(false, nil, nil, "repeat_loop", float64(st.PausedAt.Int64)), asleep: gem})
}

// Claude's permission notification wakes the engine at once (no 2 s wait);
// other events do not.
func TestAutoAcceptKickedByPermissionNotification(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Claude, "dev", "")
	token := agentToken(t, r.h, sid, "dev")
	post := func(eventType, summary string) {
		code, _ := r.post("/api/sessions/live/dev/events", map[string]any{"event_type": eventType, "summary": summary, "session_id": sid}, map[string]string{store.SessionTokenHeader: token})
		require.Equal(t, http.StatusOK, code)
	}
	kicked := func() bool {
		select {
		case <-r.a.kick:
			return true
		default:
			return false
		}
	}
	post("tool_use", "Ran: ls")
	post("notification", "Notification: Claude is waiting for your input")
	assert.False(t, kicked())
	post("notification", "Notification: Claude needs your permission to use Bash")
	assert.True(t, kicked())
}

// A time limit or source left on a row that is off is never shown.
func TestAutoAcceptFieldsHideStaleLimit(t *testing.T) {
	st := store.AutoAcceptState{Until: sql.NullInt64{Int64: 1_900_000_000, Valid: true}, Source: sql.NullString{String: "hub", Valid: true}}
	f := autoAcceptFields(at.Claude, st)
	assert.Nil(t, f["auto_accept_until"])
	assert.Nil(t, f["auto_accept_source"])
}

// QA's #3182 scenarios: for Claude only a permission/approval request with a
// PreToolUse record is answered. An MCP input dialog ("needs your input") with
// a Yes/No screen is not; a permission prompt without the record is not (one
// skipped event: the tool is unknown), which also covers a Coral restart with
// a question-shaped prompt open (the record is kept in memory).
func TestAutoAcceptClaudeNeedsAnApprovalRequestAndAPendingTool(t *testing.T) {
	yesNo := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n"
	r := newAutoAcceptRig(t)

	mcp := r.agent(at.Claude, "mcp", yesNo)
	r.enable("mcp", mcp)
	r.h.pending.set(mcp, pendingTool{ToolName: "mcp__deploy__run", Input: map[string]any{"env": "prod"}, At: r.now})
	r.notify("mcp", mcp, "Notification: Claude needs your input")
	r.run()
	assert.Empty(t, r.keys(at.Claude, mcp), "an input dialog is never answered")
	// A permission request after it is the latest: answered.
	r.notify("mcp", mcp, "Notification: Claude needs your permission to use mcp__deploy__run")
	r.run()
	assert.Equal(t, []string{"1"}, r.keys(at.Claude, mcp))

	bare := r.agent(at.Claude, "bare", autoAcceptScreen(t, "claude_bash"))
	r.enable("bare", bare)
	r.notify("bare", bare, "Notification: Claude needs your permission to use Bash")
	for i := 0; i < 3; i++ {
		r.run()
		r.now = r.now.Add(2 * time.Minute)
	}
	assert.Empty(t, r.keys(at.Claude, bare), "no PreToolUse record: not answered")
	skipped := r.events(bare, "auto_accept_skipped")
	require.Len(t, skipped, 1)
	assert.Equal(t, "Not auto-accepted: the tool is unknown", skipped[0].Summary)

	// After a restart: a question-shaped Yes/No is open, its notification is
	// in the log, the in-memory PreToolUse record is gone.
	q := r.agent(at.Claude, "q", yesNo)
	r.enable("q", q)
	r.notify("q", q, "Notification: Claude needs your input")
	after := NewAutoAcceptor(r.h)
	after.now = func() time.Time { return r.now }
	after.Start(context.Background())
	after.RunOnce(context.Background())
	r.notify("q", q, "Notification: Claude needs your permission to use AskUserQuestion")
	after.RunOnce(context.Background())
	assert.Empty(t, r.keys(at.Claude, q), "never answered after a restart either")
}

// QA's F3 (#3182): the agent acts on the key and raises its NEXT prompt before Coral has
// written its approval. The approval must not end that newer request: it is stamped with
// the moment the key was sent, so the next prompt is answered too, back to back, and a newer
// prompt Coral does not answer keeps the session waiting for the user.
func TestAutoAcceptApprovalNeverClearsANewerPrompt(t *testing.T) {
	bash := autoAcceptScreen(t, "claude_bash")
	second := strings.Replace(bash, "\n touch probe.txt\n", "\n ls -la\n", 1)
	require.NotEqual(t, classifyApproval(at.Claude, bash).FP, classifyApproval(at.Claude, second).FP, "two different prompts")

	for _, when := range []string{"during the key (0 ms)", "after the approval is recorded"} {
		t.Run(when, func(t *testing.T) {
			r := newAutoAcceptRig(t)
			sid := r.agent(at.Claude, "dev", bash)
			r.enable("dev", sid)
			r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "touch probe.txt"}, At: r.now})
			r.notify("dev", sid, "Notification: Claude needs your permission to use Bash")
			nextPrompt := func() {
				r.screen(at.Claude, sid, second)
				r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "ls -la"}, At: r.now})
				r.notify("dev", sid, "Notification: Claude needs your permission to use Bash")
			}
			if when == "during the key (0 ms)" {
				var once sync.Once
				r.term.onRaw = func(string) { once.Do(nextPrompt) }
			}
			r.run()
			require.Equal(t, []string{"1"}, r.keys(at.Claude, sid), "the first prompt")
			if when != "during the key (0 ms)" {
				nextPrompt()
			}
			assert.True(t, r.h.deriveSingleSessionState(context.Background(), sid, 0).NeedsInput, "the newer prompt is still open")
			r.now = r.now.Add(5 * time.Second)
			r.run()
			assert.Equal(t, []string{"1", "1"}, r.keys(at.Claude, sid), "the newer prompt is answered too")
		})
	}

	// A newer prompt Coral does not answer (a question) stays open for the user.
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Claude, "dev", bash)
	r.enable("dev", sid)
	r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "touch probe.txt"}, At: r.now})
	r.notify("dev", sid, "Notification: Claude needs your permission to use Bash")
	var once sync.Once
	r.term.onRaw = func(string) {
		once.Do(func() {
			r.screen(at.Claude, sid, autoAcceptScreen(t, "claude_question"))
			r.h.pending.set(sid, pendingTool{ToolName: "AskUserQuestion", At: r.now})
			r.notify("dev", sid, "Notification: Claude needs your input")
		})
	}
	r.run()
	require.Equal(t, []string{"1"}, r.keys(at.Claude, sid))
	st := r.h.deriveSingleSessionState(context.Background(), sid, 0)
	assert.True(t, st.NeedsInput, "the question still waits for the user")
	assert.Equal(t, "Notification: Claude needs your input", st.WaitingSummary)
	r.now = r.now.Add(5 * time.Second)
	r.run()
	assert.Equal(t, []string{"1"}, r.keys(at.Claude, sid), "and Coral does not answer it")
}

// QA's nit (#3182 v2.3): Sec-Fetch-Site same-origin is not enough when the browser also names a
// different page: a foreign (or opaque) Origin, or a foreign Referer, is refused. The request's
// own host passes, also as https (a dashboard behind a TLS-terminating proxy).
func TestAutoAcceptEnableRefusesAForeignOrigin(t *testing.T) {
	r := newAutoAcceptRig(t)
	sid := r.agent(at.Claude, "dev", autoAcceptScreen(t, "claude_bash"))
	host := strings.TrimPrefix(r.server.URL, "http://")
	for name, hdr := range map[string]map[string]string{
		"foreign Origin":  {"Sec-Fetch-Site": "same-origin", "Origin": "http://evil.example"},
		"opaque Origin":   {"Sec-Fetch-Site": "same-origin", "Origin": "null"},
		"foreign Referer": {"Sec-Fetch-Site": "same-origin", "Referer": "http://evil.example/page"},
		"lookalike host":  {"Sec-Fetch-Site": "same-origin", "Origin": "http://" + host + ".evil.example"},
	} {
		code, body := r.enableAs("dev", sid, hdr, nil)
		assert.Equal(t, http.StatusForbidden, code, "%s: %v", name, body)
		assert.Equal(t, "no_dashboard_provenance", body["code"], name)
	}
	for name, hdr := range map[string]map[string]string{
		"own Origin":           {"Sec-Fetch-Site": "same-origin", "Origin": "http://" + host},
		"own Origin via https": {"Sec-Fetch-Site": "same-origin", "Origin": "https://" + host},
		"own Referer":          {"Sec-Fetch-Site": "same-origin", "Referer": "http://" + host + "/#chat"},
		"no Origin at all":     {"Sec-Fetch-Site": "same-origin"},
	} {
		code, body := r.enableAs("dev", sid, hdr, nil)
		assert.Equal(t, http.StatusOK, code, "%s: %v", name, body)
	}
}

// The Orchestrator's tie case (#3182 v2.3): the newer prompt's notification carries exactly the
// instant Coral read before sending the key. Events sort by (created_at, id) and the approval is
// written later (a higher id), so a tie would put it after the notification and end it; the
// approval is stamped a microsecond earlier, so it never ties.
func TestAutoAcceptApprovalNeverTiesWithANewerPrompt(t *testing.T) {
	bash := autoAcceptScreen(t, "claude_bash")
	second := strings.Replace(bash, "\n touch probe.txt\n", "\n ls -la\n", 1)
	r := newAutoAcceptRig(t)
	instant := time.Date(2030, 1, 2, 3, 4, 5, 678901000, time.UTC)
	r.a.wallNow = func() time.Time { return instant }
	sid := r.agent(at.Claude, "dev", bash)
	r.enable("dev", sid)
	r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "touch probe.txt"}, At: r.now})
	_, err := r.h.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{AgentName: "dev", SessionID: &sid, EventType: "notification",
		Summary: "Notification: Claude needs your permission to use Bash", CreatedAt: instant.Add(-time.Second).Format(store.ISOFormat)})
	require.NoError(t, err)
	var once sync.Once
	r.term.onRaw = func(string) {
		once.Do(func() {
			r.screen(at.Claude, sid, second)
			r.h.pending.set(sid, pendingTool{ToolName: "Bash", Input: map[string]any{"command": "ls -la"}, At: r.now})
			_, err := r.h.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{AgentName: "dev", SessionID: &sid, EventType: "notification",
				Summary: "Notification: Claude needs your permission to use Bash", CreatedAt: instant.Format(store.ISOFormat)})
			require.NoError(t, err)
		})
	}
	r.run()
	require.Equal(t, []string{"1"}, r.keys(at.Claude, sid))
	approvals := r.events(sid, "auto_accept")
	require.Len(t, approvals, 1)
	assert.Less(t, approvals[0].CreatedAt, instant.Format(store.ISOFormat), "stamped before the instant it read")
	assert.True(t, r.h.deriveSingleSessionState(context.Background(), sid, 0).NeedsInput, "a notification at the very same instant is still open")
}

// The Cloud Hub sends a disable-all the moment an owner lowers a machine's access and does not wait for an enable that is still deciding: an enable that began before a
// disable-all must not turn the run on after it, or the run stays on at the lowered access with nothing left to stop it.
func TestAutoAcceptEnableThatBeganBeforeADisableAllDoesNotTurnTheRunOn(t *testing.T) {
	r := newAutoAcceptRig(t)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}
	sid := r.agent(at.Claude, "dev", "")

	// the disable-all lands while the enable has passed its checks and not yet written
	var stopped any
	r.h.onAutoEnableDeciding = func() {
		code, body := r.post("/api/auto-accept/disable-all", map[string]any{}, nil)
		require.Equal(t, http.StatusOK, code)
		stopped = body["stopped"]
	}
	code, body := r.enableAs(sid, sid, hub, nil)
	assert.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, "stopped_meanwhile", body["code"])
	assert.Equal(t, float64(0), stopped, "nothing was running yet when the disable-all ran")
	assert.False(t, r.state(sid).Enabled, "the overtaken enable never turned the run on")
	assert.Empty(t, r.events(sid, "auto_accept_enabled"), "and recorded no enable")

	// an enable that begins after the disable-all is an ordinary one
	r.h.onAutoEnableDeciding = nil
	code, body = r.enableAs(sid, sid, hub, nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.True(t, r.state(sid).Enabled)

	// and a disable-all after an enable still ends it
	code, body = r.post("/api/auto-accept/disable-all", map[string]any{}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, float64(1), body["stopped"])
	assert.False(t, r.state(sid).Enabled)
}

// #4471 (Security on #4468): a per-session Stop wins the same way. The Operator presses Stop on an agent while that agent's
// enable has passed its checks and not yet written: the enable must not turn the run on after the Stop.
func TestAutoAcceptStopOnOneAgentWinsOverItsEnableInFlight(t *testing.T) {
	r := newAutoAcceptRig(t)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}
	sid := r.agent(at.Claude, "dev", "")

	r.h.onAutoEnableDeciding = func() {
		code, _ := r.post("/api/sessions/live/dev/auto-accept/disable", map[string]any{"session_id": sid}, nil)
		require.Equal(t, http.StatusOK, code)
	}
	code, body := r.enableAs(sid, sid, hub, nil)
	assert.Equal(t, http.StatusConflict, code, body)
	assert.Equal(t, "stopped_meanwhile", body["code"])
	assert.False(t, r.state(sid).Enabled, "the Stop won: the enable in flight never turned the run on")
	assert.Empty(t, r.events(sid, "auto_accept_enabled"), "and recorded no enable")

	// an enable that begins after the Stop is an ordinary one, and a Stop after it still ends it
	r.h.onAutoEnableDeciding = nil
	code, body = r.enableAs(sid, sid, hub, nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.True(t, r.state(sid).Enabled)
	code, _ = r.post("/api/sessions/live/dev/auto-accept/disable", map[string]any{"session_id": sid}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.False(t, r.state(sid).Enabled)
}
