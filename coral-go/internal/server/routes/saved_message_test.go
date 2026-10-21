package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// "Read the whole message" of a bookmark (#5753, Security's conditions 3 and 4).

const savedFakeToken = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"

type savedSources struct {
	boardCalls, agentCalls []string
	boardText              map[string]string // "project/id" -> text
	boardErr               error
	agentText              map[string]string // "session/key" -> text
	unreachable            map[string]bool   // sessions with no transcript here
}

func (s *savedSources) board(_ context.Context, project string, id int64) (string, bool, error) {
	k := fmt.Sprintf("%s/%d", project, id)
	s.boardCalls = append(s.boardCalls, k)
	t, ok := s.boardText[k]
	return t, ok, s.boardErr
}

func (s *savedSources) agent(_ context.Context, session, agentType, key string) (string, bool, bool) {
	s.agentCalls = append(s.agentCalls, session+"/"+agentType+"/"+key)
	if s.unreachable[session] {
		return "", false, false
	}
	t, ok := s.agentText[session+"/"+key]
	return t, ok, true
}

func saveBoard(g *savedRig, project, id string) float64 {
	g.t.Helper()
	code, out := g.do("POST", "/api/saved", savedAsOperator, map[string]any{"kind": "board", "ref": project, "key": id, "title": project, "snippet": "x", "said_at": "2026-10-17T08:00:00Z"})
	require.Equal(g.t, http.StatusOK, code, out)
	return out["id"].(float64)
}

func saveAgent(g *savedRig, session, key string) float64 {
	g.t.Helper()
	m := agentSave(key, "x")
	m["ref"] = session
	code, out := g.do("POST", "/api/saved", savedAsOperator, m)
	require.Equal(g.t, http.StatusOK, code, out)
	return out["id"].(float64)
}

func read(g *savedRig, id float64, query string) (int, map[string]any) {
	g.t.Helper()
	return g.do("GET", fmt.Sprintf("/api/saved/%d/message%s", int64(id), query), savedAsOperator, nil)
}

func TestSavedMessageIsReadFromTheStoredItemOnly(t *testing.T) {
	g := newSavedRig(t)
	src := &savedSources{boardText: map[string]string{"teamA/7": "the whole board message", "teamB/7": "ANOTHER board's message 7"}, agentText: map[string]string{}}
	g.h.SetSavedSources(src.board, src.agent)
	id := saveBoard(g, "teamA", "7")
	// the reply is the bookmark's own source; a ref, key or kind in the query or the body changes nothing
	code, out := read(g, id, "?ref=teamB&key=9&kind=agent&id=1")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "the whole board message", out["text"])
	assert.Equal(t, false, out["partial"])
	assert.Nil(t, out["reason"])
	assert.Equal(t, []string{"teamA/7"}, src.boardCalls, "the source asked is the stored board and message id")
	// a board's message id that exists on ANOTHER board is not found on the bookmark's own board: gone, nothing of the other board's text
	other := saveBoard(g, "teamC", "7")
	code, out = read(g, other, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "gone", out["reason"])
	assert.Empty(t, out["text"])
	assert.NotContains(t, fmt.Sprint(out), "ANOTHER")
}

func TestSavedMessageOfAnAgentUsesItsStoredSessionAndKey(t *testing.T) {
	g := newSavedRig(t)
	k1, k2, k3 := strings.Repeat("a1", 10), strings.Repeat("b2", 10), strings.Repeat("c3", 10)
	src := &savedSources{agentText: map[string]string{"sess-1/" + k1: "what the agent said, whole"}, unreachable: map[string]bool{"sess-away": true}}
	g.h.SetSavedSources(src.board, src.agent)
	id := saveAgent(g, "sess-1", k1)
	code, out := read(g, id, "?ref=sess-away&key="+k2)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "what the agent said, whole", out["text"])
	assert.Equal(t, []string{"sess-1/claude/" + k1}, src.agentCalls, "the session, agent type and key of the stored item")
	// gone: there is a transcript and the message is not in it
	_, out = read(g, saveAgent(g, "sess-1", k3), "")
	assert.Equal(t, "gone", out["reason"])
	// not reachable: no transcript of that agent on this machine
	_, out = read(g, saveAgent(g, "sess-away", k2), "")
	assert.Equal(t, "not_reachable", out["reason"])
	assert.Empty(t, out["text"])
}

func TestSavedMessageReasonsWhenTheSourcesAreMissingOrFail(t *testing.T) {
	g := newSavedRig(t)
	// no readers wired: not reachable, never an error
	b, a := saveBoard(g, "teamA", "1"), saveAgent(g, "sess-1", strings.Repeat("a1", 10))
	for _, id := range []float64{b, a} {
		code, out := read(g, id, "")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, "not_reachable", out["reason"])
	}
	src := &savedSources{boardErr: fmt.Errorf("the board is busy")}
	g.h.SetSavedSources(src.board, src.agent)
	_, out := read(g, b, "")
	assert.Equal(t, "not_reachable", out["reason"], "a source that fails is not reachable")
	// an unknown id and a bad id
	code, _ := read(g, 99999, "")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = g.do("GET", "/api/saved/x/message", savedAsOperator, nil)
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestSavedMessageIsRedactedAndCut(t *testing.T) {
	g := newSavedRig(t)
	long := strings.Repeat("word ", 40000) // 200 KB
	src := &savedSources{boardText: map[string]string{
		"t/1": "use " + savedFakeToken + " for the call, ok",
		"t/2": long,
		// a token that straddles the 64 KiB cap: the cut drops it whole, never half of it
		"t/3": strings.Repeat("x ", maxSavedMessage/2-6) + "start " + savedFakeToken + " tail",
	}}
	g.h.SetSavedSources(src.board, src.agent)
	_, out := read(g, saveBoard(g, "t", "1"), "")
	assert.NotContains(t, fmt.Sprint(out["text"]), savedFakeToken, "a token in the source is masked")
	assert.NotContains(t, fmt.Sprint(out["text"]), savedFakeToken[:20])
	assert.Contains(t, fmt.Sprint(out["text"]), "for the call, ok")
	assert.Equal(t, false, out["partial"])

	_, out = read(g, saveBoard(g, "t", "2"), "")
	text := out["text"].(string)
	assert.LessOrEqual(t, len(text), maxSavedMessage, "the text is capped at 64 KiB")
	assert.Greater(t, len(text), maxSavedMessage-100, "and keeps what fits")
	assert.Equal(t, true, out["partial"])
	assert.Equal(t, "too_long", out["reason"])

	_, out = read(g, saveBoard(g, "t", "3"), "")
	assert.Equal(t, true, out["partial"])
	assert.NotContains(t, out["text"], savedFakeToken[:12], "no half of a token at the cut")
}

func TestSavedMessageIsTheOwnersAloneThroughTheHub(t *testing.T) {
	g := newSavedRig(t)
	src := &savedSources{boardText: map[string]string{"t/1": "text"}}
	g.h.SetSavedSources(src.board, src.agent)
	id := saveBoard(g, "t", "1")
	path := fmt.Sprintf("/api/saved/%d/message", int64(id))
	for name, hdr := range map[string]map[string]string{
		"an agent's token":   {testAgentHeader: "s1"},
		"a script":           {},
		"a forged hub stamp": {tunnel.ViaHeader: "x"},
		"a request through the hub that is not the owner's": {testDashboardHeader: "1", tunnel.ViaHeader: "1"},
	} {
		code, out := g.do("GET", path, hdr, nil)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Contains(t, []any{"operator_only", "owner_only"}, out["code"], name)
	}
	assert.Empty(t, src.boardCalls, "a refused caller reaches no source")
	// the owner through the hub is let in
	withLabel(t, cmtHubUser, nil)
	code, out := g.do("GET", path, cmtAsHubOwner, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "text", out["text"])
	// and a hub viewer, or the owner's credential with no owner marker, is refused in the words of the route
	for name, hdr := range map[string]map[string]string{"a hub viewer": cmtAsHub, "no owner marker": cmtAsOwnerThroughTunnel} {
		code, out = g.do("GET", path, hdr, nil)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "owner_only", out["code"], name)
		assert.Contains(t, fmt.Sprint(out["error"]), "saved messages", name)
	}
}
