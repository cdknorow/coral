package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #4969: the live switch of an agent's model and thinking level. Coral types the CLI's own command once the agent is between
// turns, and the stored model or level change only when the transcript confirms: a /model entry for the model, the next reply's
// effort for a level.

// dashboardPost posts as the dashboard does (a same-origin fetch), which is the operator's own request.
func dashboardPost(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	origin := url[:strings.Index(url[len("http://"):], "/")+len("http://")]
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf strings.Builder
	_, _ = io.Copy(&buf, resp.Body)
	return resp.StatusCode, buf.String()
}

type switchRig struct {
	server *testServer
	sid    string
	name   string
	path   string
	ss     *store.SessionStore
	h      *SessionsHandler
	term   *mockSessionTerminal
}

type testServer = struct {
	URL string
}

func newSwitchRig(t *testing.T, agentType string) *switchRig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	server, h, term, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	old := switchTiming
	switchTiming.poll, switchTiming.idleWait, switchTiming.modelConfirm, switchTiming.levelConfirm = 5*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { switchTiming = old })
	rig := &switchRig{server: &testServer{URL: server.URL}, sid: "abcdef01-2222-3333-4444-555555555555", ss: ss, h: h, term: term}
	rig.name = agentType + "-" + rig.sid
	work := t.TempDir()
	term.addSession(rig.name, work)
	opus := "claude-opus-5-5"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: rig.sid, AgentType: agentType, AgentName: "proj", WorkingDir: work, Model: &opus}))
	rig.path = filepath.Join(dir, "x", rig.sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(rig.path), 0o700))
	// The transcript exists from the start (a session has one before it is switched), so the reader finds it at its first look.
	require.NoError(t, os.WriteFile(rig.path, []byte(`{"sessionId":"`+rig.sid+`","type":"user","message":{"role":"user","content":"hi"},"timestamp":"2026-10-16T00:00:00Z"}`+"\n"), 0o600))
	return rig
}

func (r *switchRig) post(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	body["session_id"], body["agent_type"] = r.sid, "claude"
	code, out := dashboardPost(t, r.server.URL+"/api/sessions/live/"+r.name+"/model", body)
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	return code, m
}

// localCommand appends the two system entries Claude Code writes for a slash command it ran.
func (r *switchRig) localCommand(t *testing.T, name, args, output string) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	line := func(content string) string {
		b, _ := json.Marshal(map[string]any{"sessionId": r.sid, "type": "system", "subtype": "local_command", "content": content, "timestamp": ts})
		return string(b)
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(line("<command-name>"+name+"</command-name>\n <command-message>"+strings.TrimPrefix(name, "/")+"</command-message>\n <command-args>"+args+"</command-args>") + "\n" + line("<local-command-stdout>"+output+"</local-command-stdout>") + "\n")
	require.NoError(t, err)
}

func (r *switchRig) assistant(t *testing.T, effort string) {
	t.Helper()
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(`{"sessionId":"` + r.sid + `","type":"assistant","effort":"` + effort + `","message":{"model":"claude-sonnet-5-5","role":"assistant","content":[]},"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}` + "\n")
	require.NoError(t, err)
}

func (r *switchRig) typed() []string { return commandsForText(r.term, "/") }

// commandsForText is the typed lines that start with prefix (slash commands), in the order the terminal got them.
func commandsForText(term *mockSessionTerminal, prefix string) []string {
	var out []string
	for _, c := range term.sentCommands() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (r *switchRig) waitTyped(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, c := range r.typed() {
			if c == want {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "typed %q, got %v", want, r.typed())
}

func (r *switchRig) state(t *testing.T) *modelSwitch { return r.h.modelSwitches.get(r.sid) }

func (r *switchRig) waitState(t *testing.T, want string) *modelSwitch {
	t.Helper()
	var sw *modelSwitch
	require.Eventually(t, func() bool { sw = r.state(t); return sw != nil && sw.State == want }, 3*time.Second, 5*time.Millisecond, "state %s, got %+v", want, r.state(t))
	return sw
}

func (r *switchRig) storedModel(t *testing.T) (string, string) {
	t.Helper()
	ls, err := r.ss.GetLiveSession(context.Background(), r.sid)
	require.NoError(t, err)
	m, th := "", ""
	if ls.Model != nil {
		m = *ls.Model
	}
	if ls.Thinking != nil {
		th = *ls.Thinking
	}
	return m, th
}

func TestSwitchModelRefusals(t *testing.T) {
	r := newSwitchRig(t, "claude")
	str := func(s string) *string { return &s }
	_ = str
	for _, c := range []struct {
		name string
		body map[string]any
		code int
		sub  string
	}{
		{"nothing asked", map[string]any{}, http.StatusBadRequest, "model or thinking is required"},
		{"a flag as the model", map[string]any{"model": "--dangerously-skip-permissions"}, http.StatusBadRequest, "model must be a model id"},
		{"a space in the model", map[string]any{"model": "opus --yolo"}, http.StatusBadRequest, "model must be a model id"},
		{"an empty model", map[string]any{"model": ""}, http.StatusBadRequest, "model must be a model id"},
		{"another provider's model", map[string]any{"model": "gpt-5.5"}, http.StatusBadRequest, "cannot launch"},
		{"a level the type does not take", map[string]any{"thinking": "ultra"}, http.StatusBadRequest, "thinking must be one of"},
		{"a level with text after it", map[string]any{"thinking": "high now"}, http.StatusBadRequest, "thinking must be one of"},
	} {
		code, m := r.post(t, c.body)
		assert.Equal(t, c.code, code, c.name)
		assert.Contains(t, m["error"], c.sub, c.name)
	}
	assert.Empty(t, r.typed(), "nothing was typed")
	assert.Nil(t, r.state(t), "and no switch began")

	// An agent (its token) may not switch another agent's model.
	b, _ := json.Marshal(map[string]any{"session_id": r.sid, "agent_type": "claude", "model": "opus"})
	req, _ := http.NewRequest(http.MethodPost, r.server.URL+"/api/sessions/live/"+r.name+"/model", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(testAgentHeader, "11111111-2222-3333-4444-555555555555")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, r.typed())
}

func TestSwitchModelNeedsARestartForATypeWithNoLiveSwitch(t *testing.T) {
	r := newSwitchRig(t, "codex")
	b, _ := json.Marshal(map[string]any{"session_id": r.sid, "agent_type": "codex", "model": "gpt-5.5"})
	code, out := dashboardPost(t, r.server.URL+"/api/sessions/live/"+r.name+"/model", json.RawMessage(b))
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Contains(t, out, `"code":"needs_restart"`)
	assert.Empty(t, r.typed())
	assert.Nil(t, r.state(t))
}

func TestSwitchModelIsConfirmedByTheModelEntry(t *testing.T) {
	r := newSwitchRig(t, "claude")
	code, m := r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
	require.Equal(t, http.StatusAccepted, code, m)
	r.waitTyped(t, "/model claude-sonnet-5-5")
	sw := r.state(t)
	require.NotNil(t, sw)
	assert.Equal(t, switchPending, sw.State, "typed, not yet confirmed")
	model, _ := r.storedModel(t)
	assert.Equal(t, "claude-opus-5-5", model, "the stored model changes only when the transcript confirms")

	r.localCommand(t, "/model", "claude-sonnet-5-5", "Set model to `Sonnet 5.5` and saved as your default for new sessions")
	sw = r.waitState(t, switchDone)
	assert.Equal(t, "claude-sonnet-5-5", *sw.Model)
	model, _ = r.storedModel(t)
	assert.Equal(t, "claude-sonnet-5-5", model, "now it is stored: a restart starts with it")

	// The row says so, on the list and the info.
	var info map[string]any
	resp, err := http.Get(r.server.URL + "/api/sessions/live/" + r.name + "/info?agent_type=claude&session_id=" + r.sid)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	ms, _ := info["model_switch"].(map[string]any)
	require.NotNil(t, ms, "model_switch on the info: %v", info)
	assert.Equal(t, "done", ms["state"])
}

func TestSwitchModelRefusedByTheCLIOrNotConfirmed(t *testing.T) {
	t.Run("the CLI refuses", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		_, _ = r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
		r.waitTyped(t, "/model claude-sonnet-5-5")
		r.localCommand(t, "/model", "claude-sonnet-5-5", "Unknown model: claude-sonnet-5-5")
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "Unknown model")
		model, _ := r.storedModel(t)
		assert.Equal(t, "claude-opus-5-5", model, "a refused switch changes nothing stored")
	})
	t.Run("nothing is confirmed in time", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		_, _ = r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "did not confirm")
		model, _ := r.storedModel(t)
		assert.Equal(t, "claude-opus-5-5", model)
	})
	t.Run("an old /model entry does not confirm a new one", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		r.localCommand(t, "/model", "x", "Set model to `Old` and saved")
		time.Sleep(1100 * time.Millisecond) // sleep-ok: the stale entry must be older than the command by more than the clock allowance
		_, _ = r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "did not confirm")
	})
}

func TestSwitchLevelIsConfirmedByTheNextReply(t *testing.T) {
	r := newSwitchRig(t, "claude")
	code, m := r.post(t, map[string]any{"thinking": "high"})
	require.Equal(t, http.StatusAccepted, code, m)
	r.waitTyped(t, "/effort high")
	assert.Equal(t, switchPending, r.state(t).State)
	_, th := r.storedModel(t)
	assert.Equal(t, "", th, "not stored before the reply shows it")
	r.assistant(t, "medium")
	time.Sleep(60 * time.Millisecond) // sleep-ok: a reply at the old level does not confirm
	assert.Equal(t, switchPending, r.state(t).State)
	r.assistant(t, "high")
	r.waitState(t, switchDone)
	_, th = r.storedModel(t)
	assert.Equal(t, "high", th)
}

func TestSwitchLevelToTheDefaultAndRefusedOrUnconfirmed(t *testing.T) {
	t.Run("the default is /effort auto, confirmed by the entry", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		high := "high"
		require.NoError(t, r.ss.SetSessionThinking(context.Background(), r.sid, high))
		_, _ = r.post(t, map[string]any{"thinking": ""})
		r.waitTyped(t, "/effort auto")
		r.localCommand(t, "/effort", "auto", "Effort level set to auto")
		r.waitState(t, switchDone)
		_, th := r.storedModel(t)
		assert.Equal(t, "", th)
	})
	t.Run("the CLI refuses the level", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		_, _ = r.post(t, map[string]any{"thinking": "max"})
		r.waitTyped(t, "/effort max")
		r.localCommand(t, "/effort", "max", "Invalid effort level for this model")
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "did not take the level")
		_, th := r.storedModel(t)
		assert.Equal(t, "", th)
	})
	t.Run("no reply confirms it", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		_, _ = r.post(t, map[string]any{"thinking": "low"})
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "not confirmed")
	})
}

func TestSwitchModelThenLevelAndOneAtATime(t *testing.T) {
	r := newSwitchRig(t, "claude")
	code, _ := r.post(t, map[string]any{"model": "claude-sonnet-5-5", "thinking": "low"})
	require.Equal(t, http.StatusAccepted, code)
	code, m := r.post(t, map[string]any{"thinking": "high"})
	assert.Equal(t, http.StatusConflict, code, "a second switch while one is pending")
	assert.Equal(t, "switch_in_progress", m["code"])
	r.waitTyped(t, "/model claude-sonnet-5-5")
	assert.NotContains(t, r.typed(), "/effort low", "the level is typed after the model is confirmed")
	r.localCommand(t, "/model", "claude-sonnet-5-5", "Set model to `Sonnet 5.5`")
	r.waitTyped(t, "/effort low")
	r.assistant(t, "low")
	r.waitState(t, switchDone)
	model, th := r.storedModel(t)
	assert.Equal(t, "claude-sonnet-5-5", model)
	assert.Equal(t, "low", th)
	order := r.typed()
	assert.Equal(t, []string{"/model claude-sonnet-5-5", "/effort low"}, order)
}

func TestSwitchWaitsForTheReplyToEnd(t *testing.T) {
	r := newSwitchRig(t, "claude")
	ctx := context.Background()
	event := func(kind string) {
		_, err := r.h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "proj", SessionID: &r.sid, EventType: kind, Summary: kind})
		require.NoError(t, err)
	}
	event("prompt_submit") // mid-turn
	code, m := r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
	require.Equal(t, http.StatusAccepted, code, m)
	sw, _ := m["switch"].(map[string]any)
	assert.Equal(t, true, sw["after_reply"], "the reply says it applies after the reply")
	time.Sleep(80 * time.Millisecond) // sleep-ok: nothing may be typed while the turn runs
	assert.Empty(t, r.typed(), "nothing is typed in the middle of a turn")
	assert.True(t, r.state(t).AfterReply)
	event("stop")
	r.waitTyped(t, "/model claude-sonnet-5-5")

	t.Run("a turn that never ends", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		_, err := r.h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "proj", SessionID: &r.sid, EventType: "tool_use", Summary: "x"})
		require.NoError(t, err)
		_, _ = r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
		sw := r.waitState(t, switchFailed)
		assert.Contains(t, sw.Message, "kept working")
		assert.Empty(t, r.typed())
	})
}
