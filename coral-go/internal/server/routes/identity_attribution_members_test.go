package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Messages, subscribe, leave, reads, waits, peek and reminders read the actor (#4255 commit 5, the
// last group). Under enforce a request with no verified actor is refused 403 identity_unverified, a
// member's token is attributed with no claim at all, and in log mode nothing changes and a claimed
// caller is counted. A route whose subscriber_id names the SUBJECT (leaving, a reminder) is acted on by
// a member for itself and by the Operator for anyone, so the Operator still names it there.
func TestMemberRoutesReadTheActor(t *testing.T) {
	type route struct {
		name, method, path, body string
		subject                  bool // subscriber_id names the subject: the Operator names it
		needsClaim               bool // log mode answers 400 when the request names no one
	}
	routes := []route{
		{"post a message", "POST", "/api/board/team/messages", `{"content":"hello"}`, false, true},
		{"subscribe", "POST", "/api/board/team/subscribe", `{"job_title":"Developer"}`, true, true}, // the Operator names what it joins as
		{"mark read", "POST", "/api/board/team/messages/mark-read", `{"through_id":1}`, false, true},
		{"read", "GET", "/api/board/team/messages", ``, false, false},
		{"check unread", "GET", "/api/board/team/messages/check", ``, false, false},
		{"peek", "GET", "/api/board/team/peek?target=Dev&lines=5", ``, false, true},
		{"register a wait", "POST", "/api/board/team/waits", `{"wait_type":"message","target_id":"Orchestrator"}`, false, true},
		{"leave", "DELETE", "/api/board/team/subscribe", `{}`, true, true},
		{"set a reminder", "POST", "/api/board/team/reminder", `{"message":"m","interval_seconds":60}`, true, true},
		{"stop a reminder", "DELETE", "/api/board/team/reminder", `{}`, true, true},
	}
	unverified := func(code int, body string) bool {
		return code == http.StatusForbidden && strings.Contains(body, "identity_unverified")
	}
	required := func(code int, body string) bool {
		return code == http.StatusBadRequest && strings.Contains(body, "required")
	}
	for _, rt := range routes {
		t.Run("log/"+rt.name, func(t *testing.T) {
			a := newAttributionHarness(t) // a fresh board each: leaving a board changes the next request
			setMode(t, false)
			for _, actor := range []string{"orch", "dev", "operator", "claimed"} {
				code, body := a.callBody(actor, rt.method, rt.path, rt.body)
				assert.False(t, unverified(code, body), "log mode never refuses an unverified actor: %s as %s -> %d %s", rt.name, actor, code, body)
			}
			if rt.needsClaim {
				code, body := a.callBody("dev", rt.method, rt.path, rt.body)
				assert.True(t, required(code, body), "%s with a token and no claim in log mode -> %d %s", rt.name, code, body)
			}
			a.callBody("claimed", rt.method, rt.path, rt.body)
			assert.Contains(t, enforceDiffersSites(), "attribution "+rt.method+" "+routeOfPath(strings.SplitN(rt.path, "?", 2)[0]), "a claimed caller is counted: %s", rt.name)
		})
		t.Run("enforce/"+rt.name, func(t *testing.T) {
			a := newAttributionHarness(t)
			setMode(t, true)
			code, body := a.callBody("claimed", rt.method, rt.path, rt.body)
			assert.True(t, unverified(code, body), "no credential is refused: %s -> %d %s", rt.name, code, body)
			actors := []string{"dev", "orch"}
			if !rt.subject {
				actors = append(actors, "operator")
			}
			for _, actor := range actors {
				code, body := a.callBody(actor, rt.method, rt.path, rt.body)
				assert.False(t, unverified(code, body), "%s as %s is attributed: %d %s", rt.name, actor, code, body)
				assert.False(t, required(code, body), "%s as %s needs no claim: %d %s", rt.name, actor, code, body)
			}
		})
	}
}

// What the member routes attribute to, under enforce, with no claim: a message is the token's member's;
// a joining agent subscribes from its OWN session whatever session it names, under the name it chose.
func TestMemberRoutesAttributeToTheActor(t *testing.T) {
	a := newAttributionHarness(t)
	ctx := context.Background()
	setMode(t, true)

	// A post with no claim is the member's: the stored sender is Dev.
	code, body := a.callBody("dev", "POST", "/api/board/team/messages", `{"content":"from dev, no claim"}`)
	require.Equal(t, http.StatusOK, code, body)
	msgs, err := a.bh.bs.ListMessages(ctx, "team", 50, 0, 0)
	require.NoError(t, err)
	var sender string
	for _, m := range msgs {
		if m.Content == "from dev, no claim" {
			sender = m.SubscriberID
		}
	}
	assert.Equal(t, "Dev", sender, "the sender is the token's member")

	// A joining agent: a session on no board, naming no session at all, joins from its own.
	// (The token is minted in the harness observer's own store.)
	joining := a.mintToken(t, "new", "sid-new", "claude-new")
	code, body = a.callBodyWithToken(joining, "POST", "/api/board/team/subscribe", `{"subscriber_id":"New One","job_title":"Developer"}`)
	require.Equal(t, http.StatusOK, code, body)
	sub, err := a.bh.bs.GetProjectSubscription(ctx, "team", "New One")
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "claude-new", sub.SessionName, "from the token's own session, though the request named none")

	// A member leaving with no claim leaves as itself.
	code, body = a.callBody("dev", "DELETE", "/api/board/team/subscribe", `{}`)
	require.Equal(t, http.StatusOK, code, body)
	gone, err := a.bh.bs.GetProjectSubscription(ctx, "team", "Dev")
	require.NoError(t, err)
	assert.True(t, gone == nil || gone.IsActive == 0, "Dev left")

	// The Operator names the member it removes (the subject), and a member cannot remove another.
	code, _ = a.callBody("operator", "DELETE", "/api/board/team/subscribe", `{"subscriber_id":"Orchestrator"}`)
	assert.Equal(t, http.StatusOK, code, "the Operator removes the member it names")
}

// Under enforce a Hub user joins as the name the tunnel decided, a Hub request with no name is
// refused, and nobody but the Hub operator joins as the Operator (#4287, Security INFO 1 on #4255).
func TestAttributedJoinForAHubActor(t *testing.T) {
	join := func(a Actor, name, session string) (string, string, int) {
		r := withActor(httptest.NewRequest("POST", "/api/board/team/subscribe", nil), a)
		w := httptest.NewRecorder()
		n, s, ok := attributedJoin(w, r, name, session)
		if ok {
			return n, s, http.StatusOK
		}
		return n, s, w.Code
	}
	hubUser := Actor{Kind: ActorHub, Name: "alice", HubUser: "alice"}

	setMode(t, true)
	n, s, code := join(hubUser, "", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "alice", n, "no claim: the Hub user's own name")
	assert.Empty(t, s)
	n, _, code = join(hubUser, "Lead", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "alice", n, "a claim of another member's name is not taken")
	for _, claim := range []string{"Operator", "operator", " OPERATOR "} {
		_, _, code = join(hubUser, claim, "")
		assert.Equal(t, http.StatusForbidden, code, "a Hub user claiming %q", claim)
	}
	_, _, code = join(Actor{Kind: ActorHub}, "alice", "")
	assert.Equal(t, http.StatusForbidden, code, "a Hub request with no name")
	n, _, code = join(Actor{Kind: ActorHub, Name: "Operator", HubUser: "op"}, "Operator", "")
	assert.Equal(t, http.StatusOK, code, "the Hub operator may join as the Operator")
	assert.Equal(t, "Operator", n)

	setMode(t, false)
	n, _, code = join(hubUser, "Lead", "sess")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Lead", n, "log mode is unchanged: the claim")
	before := enforceDiffersSnapshot()
	join(Actor{Kind: ActorHub}, "alice", "")
	assert.NotEqual(t, before, enforceDiffersSnapshot(), "a nameless Hub request is counted in log mode")
}
