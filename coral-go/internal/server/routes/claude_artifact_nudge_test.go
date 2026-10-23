package routes

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

type typed struct{ session, key, text string }

type nudgeRig struct {
	ss    *store.SessionStore
	n     *ClaudeArtifactNudger
	mu    sync.Mutex
	typed []typed
}

func (r *nudgeRig) sent() []typed {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]typed(nil), r.typed...)
}

func newNudgeRig(t *testing.T) *nudgeRig {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "n.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	r := &nudgeRig{ss: store.NewSessionStore(db)}
	r.n = r.nudger()
	return r
}

// nudger is a nudger on the rig's store (a second one is a restarted Coral).
func (r *nudgeRig) nudger() *ClaudeArtifactNudger {
	n := NewClaudeArtifactNudger(r.ss, nil)
	n.sync = true
	n.deliver = func(_ context.Context, _ ptymanager.SessionTerminal, name, _, _, key, text, _ string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.typed = append(r.typed, typed{name, key, text})
		return nil
	}
	return n
}

func owner(n string) *store.SessionTokenOwner {
	id := "11111111-2222-3333-4444-00000000" + n
	return &store.SessionTokenOwner{SessionID: id, SessionName: "claude-" + id}
}

func publish(file string) map[string]any {
	return map[string]any{"action": "publish", "file_path": file}
}

func TestClaudeArtifactPublishNudgesOncePerArtifact(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	h := &SessionsHandler{artifactNudge: r.n}
	o := owner("0001")

	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tu1", publish("/w/page.html"))
	require.Len(t, r.sent(), 1, "a publish nudges")
	assert.Equal(t, "claude-11111111-2222-3333-4444-000000000001", r.sent()[0].session)
	assert.Equal(t, "claude-artifact:11111111-2222-3333-4444-000000000001", r.sent()[0].key, "one gate key per session: a burst is one held reminder")
	assert.Equal(t, claudeArtifactNudgeText, r.sent()[0].text, "the text is the fixed template")
	assert.NotContains(t, r.sent()[0].text, "page.html", "nothing of the file, the page or an id is typed")

	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tu2", publish("/w/./page.html"))
	assert.Len(t, r.sent(), 1, "the same file published again is the same artifact")
	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tu3", publish("/w/other.html"))
	assert.Len(t, r.sent(), 2, "a different artifact gets its own reminder")

	restarted := &SessionsHandler{artifactNudge: r.nudger()}
	restarted.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tu4", publish("/w/page.html"))
	assert.Len(t, r.sent(), 2, "a restart does not repeat it")

	h.remindClaudeArtifact(ctx, owner("0002"), "tool_use", "Artifact", "tu5", publish("/w/page.html"))
	assert.Len(t, r.sent(), 3, "another agent's own file is its own artifact")
}

func TestClaudeArtifactOnlyAPublishCounts(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	h := &SessionsHandler{artifactNudge: r.n}
	o := owner("0001")
	for _, action := range []string{"read", "list", "open", "delete", "pin", "unpin", "quickstart"} {
		h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tu-"+action, map[string]any{"action": action, "file_path": "/w/" + action})
	}
	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tuf", map[string]any{"action": "publish", "file_path": "/w/f.html", "failed": true})
	h.remindClaudeArtifact(ctx, o, "tool_use", "Bash", "tub", publish("/w/b.html"))
	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "", map[string]any{"action": "publish"})
	h.remindClaudeArtifact(ctx, nil, "tool_use", "Artifact", "tun", publish("/w/n.html"))
	assert.Empty(t, r.sent(), "a read, list, open, delete, pin, unpin or quickstart, a failed call, another tool, a call naming nothing, or no agent: no reminder")
	h.remindClaudeArtifact(ctx, o, "tool_use", "Artifact", "tuo", map[string]any{"file_path": "/w/old.html"})
	assert.Len(t, r.sent(), 1, "a call from an older hook with no action is the default, a publish")
}

func TestClaudeArtifactSettingOffMeansNothing(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	require.NoError(t, r.ss.SetSetting(ctx, settings.NudgeClaudeArtifacts, "false"))
	h := &SessionsHandler{artifactNudge: r.n}
	h.remindClaudeArtifact(ctx, owner("0001"), "tool_use", "Artifact", "tu1", publish("/w/page.html"))
	h.remindClaudeArtifact(ctx, owner("0001"), "stop", "", "", map[string]any{"claude_artifact_ids": []any{"abcdefgh1"}})
	r.n.FromLinks(ctx, owner("0001"), []string{"abcdefgh2"})
	assert.Empty(t, r.sent())

	require.NoError(t, r.ss.SetSetting(ctx, settings.NudgeClaudeArtifacts, "true"))
	h.remindClaudeArtifact(ctx, owner("0001"), "tool_use", "Artifact", "tu1", publish("/w/page.html"))
	assert.Len(t, r.sent(), 1, "on again: the publish that was not counted while off is reminded now")
}

func TestClaudeArtifactSettingIsOnByDefault(t *testing.T) {
	r := newNudgeRig(t)
	h := &SessionsHandler{artifactNudge: r.n}
	h.remindClaudeArtifact(context.Background(), owner("0001"), "tool_use", "Artifact", "tu1", publish("/w/page.html"))
	assert.Len(t, r.sent(), 1, "no setting stored: on")
}

func TestClaudeArtifactLinksNudgeTheAgentOnceAcrossTheBoard(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()

	r.n.FromLinks(ctx, owner("0001"), []string{"aaaaaaaa1"})
	require.Len(t, r.sent(), 1, "an agent's own link reminds it")
	assert.Equal(t, claudeArtifactNudgeText, r.sent()[0].text)
	assert.NotContains(t, r.sent()[0].text, "aaaaaaaa1", "the typed bytes are the template: no id")
	r.n.FromLinks(ctx, owner("0001"), []string{"aaaaaaaa1"})
	r.n.FromLinks(ctx, owner("0002"), []string{"aaaaaaaa1"})
	assert.Len(t, r.sent(), 1, "the same link again, or passed along by another agent, reminds nobody")

	r.n.FromLinks(ctx, nil, []string{"operator12"})
	assert.Len(t, r.sent(), 1, "the Operator's link reminds nobody")
	r.n.FromLinks(ctx, owner("0002"), []string{"operator12"})
	assert.Len(t, r.sent(), 1, "and an agent repeating the Operator's link is not reminded")

	r.n.FromLinks(ctx, owner("0003"), []string{"bbbbbbbb1", "cccccccc1", "dddddddd1"})
	assert.Len(t, r.sent(), 2, "one reminder for a post with several new links")
	r.n.FromLinks(ctx, owner("0003"), []string{"cccccccc1"})
	assert.Len(t, r.sent(), 3, "a link the same post did not remind for is still new later")

	r.n.FromLinks(ctx, owner("0004"), []string{"short", "../../etc/passwd", "bad id with spaces"})
	assert.Len(t, r.sent(), 3, "an id of the wrong shape is ignored")
}

func TestClaudeArtifactPublishOfAKnownPageIsItsLink(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	h := &SessionsHandler{artifactNudge: r.n}
	h.remindClaudeArtifact(ctx, owner("0001"), "tool_use", "Artifact", "tu1", map[string]any{"action": "publish", "url": "https://claude.ai/artifact/abcdefgh1"})
	require.Len(t, r.sent(), 1)
	r.n.FromLinks(ctx, owner("0001"), []string{"abcdefgh1"})
	assert.Len(t, r.sent(), 1, "posting the link of a page it just published is not a second reminder")
}

func TestClaudeArtifactStopCarriesLinksFromTheReply(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	h := &SessionsHandler{artifactNudge: r.n}
	h.remindClaudeArtifact(ctx, owner("0001"), "stop", "", "", map[string]any{"claude_artifact_ids": []any{"abcdefgh1", 7, "no", "../x/../../y1234"}})
	assert.Len(t, r.sent(), 1)
	h.remindClaudeArtifact(ctx, owner("0001"), "stop", "", "", map[string]any{})
	h.remindClaudeArtifact(ctx, owner("0001"), "stop", "", "", nil)
	h.remindClaudeArtifact(ctx, owner("0001"), "notification", "", "", map[string]any{"claude_artifact_ids": []any{"zzzzzzzz1"}})
	assert.Len(t, r.sent(), 1, "only a stop event carries reply links; an empty or other event nothing")
}

func TestClaudeArtifactNudgerNilIsQuiet(t *testing.T) {
	h := &SessionsHandler{}
	h.remindClaudeArtifact(context.Background(), owner("0001"), "tool_use", "Artifact", "tu1", publish("/w/p.html"))
	var n *ClaudeArtifactNudger
	n.FromLinks(context.Background(), owner("0001"), []string{"abcdefgh1"})
	n.FromPublish(context.Background(), owner("0001"), "file:/x")
}

// Signal 1 arrives on the events route, bound to the caller's own session: another agent's token cannot make Coral type into a session it does not own, and
// an event is reminded for the token's session whatever name the path or the body gives (Security's condition 3).
func TestClaudeArtifactEventIsReminderForTheTokensOwnSessionOnly(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	rig := &nudgeRig{ss: ss}
	h.SetArtifactNudger(rig.nudger())
	sidA, sidB := "aaaaaaaa-6583-0000-0000-000000000001", "bbbbbbbb-6583-0000-0000-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidA, AgentType: "claude", AgentName: "alpha", WorkingDir: "/tmp/shared"}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidB, AgentType: "claude", AgentName: "beta", WorkingDir: "/tmp/shared"}))
	tokA := agentToken(t, h, sidA, "claude-"+sidA)
	post := func(name, body string, tok string) int {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/"+name+"/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set(store.SessionTokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	ev := func(sid string) string {
		return `{"event_type":"tool_use","tool_name":"Artifact","summary":"Used Artifact","tool_use_id":"tu1","session_id":"` + sid + `","detail_json":{"action":"publish","file_path":"/w/page.html"}}`
	}
	assert.Equal(t, http.StatusForbidden, post("beta", ev(sidB), tokA), "A's token cannot post an Artifact event for B")
	assert.Equal(t, http.StatusUnauthorized, post("beta", ev(sidB), ""), "no credential: refused")
	assert.Empty(t, rig.sent(), "neither reminded anyone")

	require.Equal(t, http.StatusOK, post("beta", ev(""), tokA), "A's own event, with B's name in the path")
	require.Len(t, rig.sent(), 1)
	assert.Equal(t, "claude-"+sidA, rig.sent()[0].session, "typed into A's session, never B's")

	assert.Equal(t, http.StatusOK, post("alpha", ev(sidA), tokA))
	assert.Len(t, rig.sent(), 1, "the same file again is not a second reminder")
}
