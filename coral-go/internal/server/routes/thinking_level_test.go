package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// #4969: a thinking level (reasoning effort) is chosen at launch, carried by restart and resume, defaulted from Settings, and given
// to the CLI as its own flag. Claude is the first type that takes one.

func postThinkingJSON(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// effortArgs counts the "--effort" arguments in the commands sent and returns the word after the last.
func effortArgs(cmds []string) (n int, last string) {
	for _, c := range cmds {
		words := strings.Fields(strings.NewReplacer("'", "", `"`, "").Replace(c))
		for i, w := range words {
			if w == "--effort" || strings.HasPrefix(w, "--effort=") {
				n++
				if w == "--effort" && i+1 < len(words) {
					last = words[i+1]
				}
			}
		}
	}
	return
}

// commandsFor is the commands typed into terminals for one session: the ones that name its id (the map of all sessions' commands has
// no stable order, and a launch may type into a name other than the session's).
func commandsFor(term *mockSessionTerminal, sessionID string) []string {
	var out []string
	for _, c := range term.sentCommands() {
		if strings.Contains(c, sessionID) {
			out = append(out, c)
		}
	}
	return out
}

func liveThinking(t *testing.T, ss *store.SessionStore, id string) string {
	t.Helper()
	ls, err := ss.GetLiveSession(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, ls)
	if ls.Thinking == nil {
		return ""
	}
	return *ls.Thinking
}

func TestLaunchGivesTheCLIItsThinkingLevel(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	ctx := context.Background()
	// launch starts an agent and returns its id and the --effort words typed into its terminal.
	launch := func(body map[string]any) (id string, n int, level string) {
		body["working_dir"] = t.TempDir()
		code, out := postThinkingJSON(t, server.URL+"/api/sessions/launch", body)
		require.Equal(t, http.StatusOK, code, out)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		n, level = effortArgs(commandsFor(term, m["session_id"].(string)))
		return m["session_id"].(string), n, level
	}

	id, n, level := launch(map[string]any{"agent_type": "claude", "thinking": "high"})
	assert.Equal(t, 1, n, "one --effort")
	assert.Equal(t, "high", level)
	assert.Equal(t, "high", liveThinking(t, ss, id), "the level is stored on the agent")

	// No level asked and no default: the CLI gets no flag.
	id, n, _ = launch(map[string]any{"agent_type": "claude"})
	assert.Equal(t, 0, n, "no --effort without a level")
	assert.Equal(t, "", liveThinking(t, ss, id))

	// The Settings default for the type applies, and a request's level wins over it.
	require.NoError(t, ss.SetSetting(ctx, "default_thinking_claude", "low"))
	id, n, level = launch(map[string]any{"agent_type": "claude"})
	assert.Equal(t, 1, n)
	assert.Equal(t, "low", level, "the saved default")
	assert.Equal(t, "low", liveThinking(t, ss, id))
	_, _, level = launch(map[string]any{"agent_type": "claude", "thinking": "max"})
	assert.Equal(t, "max", level, "the request wins over the default")

	// A user's own --effort in the flags is replaced by the level, never doubled.
	_, n, level = launch(map[string]any{"agent_type": "claude", "thinking": "medium", "flags": []string{"--effort", "low"}})
	assert.Equal(t, 1, n)
	assert.Equal(t, "medium", level)

	// A bare --effort in the flags (no value) does not swallow the flag after it: the permission flag stays a flag.
	id, n, level = launch(map[string]any{"agent_type": "claude", "thinking": "low", "flags": []string{"--effort", "--verbose"}})
	assert.Equal(t, 1, n)
	assert.Equal(t, "low", level)
	assert.Contains(t, strings.Join(commandsFor(term, id), " "), "--verbose", "the flag after a bare --effort stays")

	// A saved default that no longer fits the type is ignored (the CLI's own default applies).
	require.NoError(t, ss.SetSetting(ctx, "default_thinking_claude", "ultra"))
	_, n, _ = launch(map[string]any{"agent_type": "claude"})
	assert.Equal(t, 0, n)
}

func TestLaunchRefusesAThinkingLevelTheTypeDoesNotTake(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	for _, c := range []struct{ typ, level string }{
		{"claude", "ultra"}, {"claude", "HIGH"}, {"claude", "high --model x"}, {"claude", "--yolo"}, {"claude", "off"},
		{"codex", "max"}, {"agy", "minimal"}, {"gemini", "high"}, {"pi", "high"}, {"terminal", "high"},
	} {
		code, out := postThinkingJSON(t, server.URL+"/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": c.typ, "thinking": c.level})
		assert.Equal(t, http.StatusBadRequest, code, "%s %q: %s", c.typ, c.level, out)
		assert.Contains(t, out, `"field":"thinking"`)
	}
	assert.Empty(t, term.sentCommands(), "nothing launched")
}

func TestTeamLaunchThinkingLevel(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "default_thinking_claude", "low"))
	n := 0
	team := func(agents ...map[string]any) (int, string) {
		n++
		return postThinkingJSON(t, server.URL+"/api/sessions/launch-team", map[string]any{"board_name": "think" + string(rune('a'+n)), "working_dir": t.TempDir(), "agents": agents})
	}
	code, out := team(map[string]any{"name": "Dev", "agent_type": "claude", "thinking": "high"}, map[string]any{"name": "QA", "agent_type": "claude"})
	require.Equal(t, http.StatusOK, code, out)
	var levels []string
	for _, c := range term.sentCommands() {
		_, l := effortArgs([]string{c})
		levels = append(levels, l)
	}
	assert.ElementsMatch(t, []string{"high", "low"}, levels, "a member's own level, else the default")

	before := len(term.sentCommands())
	code, out = team(map[string]any{"name": "Bad", "agent_type": "claude", "thinking": "ultra"})
	assert.Equal(t, http.StatusBadRequest, code, out)
	assert.Contains(t, out, "thinking must be one of")
	code, out = team(map[string]any{"name": "Bad", "agent_type": "pi", "thinking": "high"})
	assert.Equal(t, http.StatusBadRequest, code, out)
	assert.Equal(t, before, len(term.sentCommands()), "nothing of a refused team starts")
}

func TestDefaultThinkingSettingTakesOnlyTheTypesLevels(t *testing.T) {
	server, _ := setupSystemTestServer(t)
	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, l := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		assert.Equal(t, http.StatusOK, put(`{"default_thinking_claude":"`+l+`"}`), l)
	}
	assert.Equal(t, http.StatusBadRequest, put(`{"default_thinking_claude":"ultra"}`))
	assert.Equal(t, http.StatusBadRequest, put(`{"default_thinking_claude":5}`))
	assert.Equal(t, http.StatusBadRequest, put(`{"default_thinking_pi":"high"}`), "a type with no level yet")
	assert.Equal(t, http.StatusOK, put(`{"default_thinking_pi":""}`))
	assert.Equal(t, http.StatusOK, put(`{"default_thinking_codex":"minimal"}`), "codex takes its own levels (#5480)")
	assert.Equal(t, http.StatusBadRequest, put(`{"default_thinking_codex":"max"}`), "claude's level is not codex's")
	assert.Equal(t, http.StatusOK, put(`{"default_thinking_agy":"max"}`))
	assert.Equal(t, http.StatusBadRequest, put(`{"default_thinking_nope":"high"}`), "an unknown type")
}

func TestRestartAndResumeKeepOrChangeTheThinkingLevel(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, op := range []string{"restart", "resume"} {
		t.Run(op, func(t *testing.T) {
			server, _, term, ss := setupSessionsTestServer(t)
			stubCLIPaths(t, ss)
			ctx := context.Background()
			dir := t.TempDir()
			next := 0
			start := func(level string, flags ...string) string {
				next++
				id := "aaaaaaaa-0000-0000-0000-00000000000" + string(rune('0'+next))
				term.addSession(naming.SessionName("claude", id), dir)
				th := level
				require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "proj", WorkingDir: dir, Thinking: &th, Flags: store.MarshalFlags(flags)}))
				return id
			}
			// call restarts (or resumes) the agent and returns the reply, the new id and the --effort words typed into the new terminal.
			call := func(id string, extra map[string]any) (code int, out, fresh string, n int, level string) {
				body := map[string]any{"agent_type": "claude", "session_id": id}
				if op == "resume" {
					body = map[string]any{"agent_type": "claude", "session_id": "dddddddd-2222-3333-4444-555555555555", "current_session_id": id}
				}
				for k, v := range extra {
					body[k] = v
				}
				code, out = postThinkingJSON(t, server.URL+"/api/sessions/live/"+naming.SessionName("claude", id)+"/"+op, body)
				var m struct {
					SessionID string `json:"session_id"`
				}
				json.Unmarshal([]byte(out), &m)
				if m.SessionID != "" {
					n, level = effortArgs(commandsFor(term, m.SessionID))
				}
				return code, out, m.SessionID, n, level
			}

			// The stored level (and its --effort in the stored flags) is kept, once.
			id := start("high", "--effort", "high")
			code, out, fresh, n, level := call(id, nil)
			require.Equal(t, http.StatusOK, code, out)
			assert.Equal(t, 1, n, "one --effort after the %s", op)
			assert.Equal(t, "high", level)
			assert.Equal(t, "high", liveThinking(t, ss, fresh))

			if op == "restart" { // resume's request has no level of its own
				// A new level in the request replaces it.
				code, out, fresh2, n, level := call(fresh, map[string]any{"thinking": "low"})
				require.Equal(t, http.StatusOK, code, out)
				assert.Equal(t, 1, n)
				assert.Equal(t, "low", level)
				assert.Equal(t, "low", liveThinking(t, ss, fresh2))

				// An empty one clears it: no flag, and the next restart has none.
				code, out, fresh3, n, _ := call(fresh2, map[string]any{"thinking": ""})
				require.Equal(t, http.StatusOK, code, out)
				assert.Equal(t, 0, n, "cleared")
				assert.Equal(t, "", liveThinking(t, ss, fresh3))
				code, out, fresh4, n, _ := call(fresh3, nil)
				require.Equal(t, http.StatusOK, code, out)
				assert.Equal(t, 0, n)
				assert.Equal(t, "", liveThinking(t, ss, fresh4))

				// A level the type does not take is refused with the old agent untouched.
				before := len(term.sentCommands())
				code, out, _, _, _ = call(fresh4, map[string]any{"thinking": "ultra"})
				assert.Equal(t, http.StatusBadRequest, code, out)
				assert.Contains(t, out, `"field":"thinking"`)
				assert.Equal(t, before, len(term.sentCommands()), "nothing was started")
				ls, err := ss.GetLiveSession(ctx, fresh4)
				require.NoError(t, err)
				assert.Equal(t, "active", ls.Status, "the agent keeps running")
			}

			// An agent started with no level, whose stored flags hold the user's own --effort, keeps that flag (it is not ours to remove).
			if op == "restart" {
				id2 := start("", "--effort", "low")
				code, out, _, n, level = call(id2, nil)
				require.Equal(t, http.StatusOK, code, out)
				assert.Equal(t, 1, n)
				assert.Equal(t, "low", level)
			}
		})
	}
}
