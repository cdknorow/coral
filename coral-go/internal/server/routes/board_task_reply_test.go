package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The reply-to-task route (#5463) over the real handler and store. A test actor header stands in for the identity layer: "token:<member>" is an agent's token, "operator" the
// dashboard; a request with neither and with the tunnel's stamp is Coral Hub.
type replyEnv struct {
	t   *testing.T
	srv *httptest.Server
	h   *BoardHandler
	bs  *board.Store
}

func newReplyEnv(t *testing.T) *replyEnv {
	t.Helper()
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch a := req.Header.Get("X-Test-Actor"); {
			case strings.HasPrefix(a, "token:"):
				req = withActor(req, Actor{Kind: ActorToken, Name: strings.TrimPrefix(a, "token:")})
			case a == "operator":
				req = withActor(req, Actor{Kind: ActorOperator, Name: "Operator"})
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Post("/api/board/{project}/messages", h.PostMessage)
	r.Get("/api/board/{project}/messages/all", h.ListAllMessages)
	r.Post("/api/board/{project}/tasks/{taskID}/reply", h.ReplyToTask)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	was := identityActorEnforced()
	setIdentityActorEnforced(true)
	t.Cleanup(func() { setIdentityActorEnforced(was) })
	return &replyEnv{t: t, srv: srv, h: h, bs: bs}
}

func (e *replyEnv) join(project string, names ...string) {
	e.t.Helper()
	for _, n := range names {
		_, err := e.bs.Subscribe(context.Background(), project, n, n, "claude-"+strings.ReplaceAll(n, " ", "-"), nil, "mentions")
		require.NoError(e.t, err)
	}
}

func (e *replyEnv) task(project, createdBy string) int64 {
	e.t.Helper()
	tk, err := e.bs.CreateTask(context.Background(), project, "A task", "its body", "medium", createdBy)
	require.NoError(e.t, err)
	return tk.ID
}

func (e *replyEnv) do(method, path, body string, hdr map[string]string) (int, map[string]any, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewBufferString(body))
	require.NoError(e.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out, b
}

func hubReplyHdr(user string) map[string]string {
	return map[string]string{tunnel.ViaHeader: "1", tunnel.HubUserHeader: user}
}

func (e *replyEnv) messages(project string, query string) []board.Message {
	e.t.Helper()
	code, _, b := e.do("GET", "/api/board/"+project+"/messages/all"+query, "", nil)
	require.Equal(e.t, 200, code, string(b))
	var out []board.Message
	require.NoError(e.t, json.Unmarshal(b, &out), string(b))
	return out
}

func TestAReplyIsALinkedMessageThatNotifiesTheCreator(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev", "Frontend Dev")
	id := e.task("p", "Backend Dev")
	code, out, b := e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"on it, ETA tomorrow"}`, map[string]string{"X-Test-Actor": "token:Frontend Dev"})
	require.Equal(t, 200, code, string(b))
	assert.Equal(t, true, out["ok"])
	assert.EqualValues(t, id, out["task_id"])
	assert.Equal(t, []any{"Backend Dev"}, out["notified"])
	assert.NotContains(t, out, "warnings")

	msgs := e.messages("p", "?task_id="+itoa(id))
	require.Len(t, msgs, 1)
	assert.Equal(t, "@Backend Dev: on it, ETA tomorrow", msgs[0].Content)
	assert.Equal(t, "Frontend Dev", msgs[0].SubscriberID, "the sender is the verified actor")
	require.NotNil(t, msgs[0].TaskID)
	assert.Equal(t, id, *msgs[0].TaskID)
	got, err := e.bs.ReadMessages(context.Background(), "p", "Backend Dev", 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "the existing mention delivery reaches the creator")
	assert.Equal(t, msgs[0].ID, got[0].ID)
}

func idText(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestAReplyNeverChangesTheTask(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev", "Frontend Dev")
	id := e.task("p", "Backend Dev")
	before, err := e.bs.GetTask(context.Background(), "p", id)
	require.NoError(t, err)
	e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"hello"}`, map[string]string{"X-Test-Actor": "token:Frontend Dev"})
	after, err := e.bs.GetTask(context.Background(), "p", id)
	require.NoError(t, err)
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, before.Revision, after.Revision)
	assert.Equal(t, before.AssignedTo, after.AssignedTo)
}

func TestAReplyToATaskWhoseCreatorCannotBeTaggedExactlyIsStoredWithNoMention(t *testing.T) {
	e := newReplyEnv(t)
	hdr := map[string]string{"X-Test-Actor": "token:Frontend Dev"}

	// the creator left, and an active member has the shorter name
	e.join("p", "Backend Dev", "Backend Dev 2", "Frontend Dev")
	_, err := e.bs.Unsubscribe(context.Background(), "p", "Backend Dev 2")
	require.NoError(t, err)
	id := e.task("p", "Backend Dev 2")
	code, out, _ := e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"any news?"}`, hdr)
	require.Equal(t, 200, code)
	assert.Equal(t, []any{}, out["notified"])
	assert.Equal(t, []any{"creator_not_reachable"}, out["warnings"])
	msgs := e.messages("p", "?task_id="+itoa(id))
	require.Len(t, msgs, 1)
	assert.Equal(t, "any news?", msgs[0].Content, "no @ is added: the active 'Backend Dev' is not tagged")
	got, _ := e.bs.ReadMessages(context.Background(), "p", "Backend Dev", 10)
	assert.Empty(t, got, "and nobody was notified by it")

	// a job-title collision, and the spaceless form
	e2 := newReplyEnv(t)
	e2.join("p", "Frontend Dev", "BackendDev", "Backend Dev")
	id2 := e2.task("p", "BackendDev")
	_, out, _ = e2.do("POST", "/api/board/p/tasks/"+itoa(id2)+"/reply", `{"content":"x"}`, hdr)
	assert.Equal(t, []any{}, out["notified"], "'@BackendDev ' is also the spaceless form of 'Backend Dev'")
}

func TestAHubUserRepliesAsItselfWhateverTheBodySays(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev", "Orchestrator")
	id := e.task("p", "Backend Dev")
	code, out, b := e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"from the hub","subscriber_id":"Orchestrator","as":"Operator","created_by":"Orchestrator"}`, hubReplyHdr("hub-user-1234abcd"))
	require.Equal(t, 200, code, string(b))
	assert.Equal(t, []any{"Backend Dev"}, out["notified"])
	msgs := e.messages("p", "?task_id="+itoa(id))
	require.Len(t, msgs, 1)
	assert.Equal(t, "hub-user-1234abcd", msgs[0].SubscriberID, "the stamped hub user, not the name in the body")
	require.NotNil(t, msgs[0].Origin)
	assert.Equal(t, board.OriginHubUser, *msgs[0].Origin)
	require.NotNil(t, msgs[0].HubUser)
	assert.Equal(t, "hub-user-1234abcd", *msgs[0].HubUser)
	assert.True(t, strings.HasPrefix(msgs[0].Content, "@Backend Dev: "))
	assert.NotContains(t, msgs[0].Content, "Operator")

	// Even where Coral Hub may act as the Operator for a post, a reply is never sent as the Operator: its body has no sender to ask with (a hub operator reply is a decision for later).
	e.h.SetHubOperatorAllowed(func(*http.Request) bool { return true })
	code, _, b = e.do("POST", "/api/board/p/tasks/"+idText(id)+"/reply", `{"content":"as the operator?","as":"Operator","subscriber_id":"Operator"}`, hubReplyHdr("hub-user-1234abcd"))
	require.Equal(t, 200, code, string(b))
	for _, m := range e.messages("p", "?task_id="+idText(id)) {
		assert.Equal(t, "hub-user-1234abcd", m.SubscriberID)
		require.NotNil(t, m.Origin)
		assert.Equal(t, board.OriginHubUser, *m.Origin)
		assert.NotContains(t, m.Content, "Operator")
	}
	e.h.SetHubOperatorAllowed(nil)

	// a hub write without a valid hub user is refused as a post is
	code, _, _ = e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"x"}`, map[string]string{tunnel.ViaHeader: "1"})
	assert.Equal(t, 400, code)
	code, _, _ = e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"x"}`, map[string]string{tunnel.ViaHeader: "1", tunnel.HubUserHeader: "not-a-hub-user"})
	assert.Equal(t, 400, code)
	assert.Len(t, e.messages("p", "?task_id="+itoa(id)), 2, "the hub user's two replies; nothing was posted by the refused ones")
}

func TestAReplyToATaskOfAnotherBoardIsA404AndPostsNothing(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev")
	e.join("q", "Frontend Dev")
	id := e.task("q", "Frontend Dev")
	hdr := map[string]string{"X-Test-Actor": "token:Backend Dev"}
	code, _, _ := e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"x"}`, hdr)
	assert.Equal(t, 404, code, "the task is looked up by the URL's board")
	assert.Empty(t, e.messages("p", ""))
	assert.Empty(t, e.messages("q", ""))
	// a task that does not exist, on a board that does not exist: 404 and no board is made
	code, _, _ = e.do("POST", "/api/board/brand-new/tasks/1/reply", `{"content":"x"}`, hdr)
	assert.Equal(t, 404, code)
	boards, _ := e.bs.GetBoard(context.Background(), "brand-new")
	assert.Nil(t, boards)
	code, _, _ = e.do("POST", "/api/board/p/tasks/abc/reply", `{"content":"x"}`, hdr)
	assert.Equal(t, 400, code)
	code, _, _ = e.do("POST", "/api/board/p/tasks/0/reply", `{"content":"x"}`, hdr)
	assert.Equal(t, 400, code)
}

func TestARepliesToAnOperatorTaskAreMentionedForTheDashboard(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev")
	id := e.task("p", "Operator")
	code, out, _ := e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"done, please look"}`, map[string]string{"X-Test-Actor": "token:Backend Dev"})
	require.Equal(t, 200, code)
	assert.Equal(t, []any{"Operator"}, out["notified"])
	msgs := e.messages("p", "?task_id="+itoa(id))
	require.Len(t, msgs, 1)
	assert.Equal(t, "@Operator: done, please look", msgs[0].Content, "@Operator is what the dashboard's notifications read")
}

func TestAReplyWithNoVerifiedSenderOrNoTextIsRefused(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev")
	id := e.task("p", "Backend Dev")
	path := "/api/board/p/tasks/" + itoa(id) + "/reply"
	// under enforcement a local request with no verified actor is refused by the identity rule; with it off the reply still has no body field to name a sender
	code, _, _ := e.do("POST", path, `{"content":"x"}`, nil)
	assert.GreaterOrEqual(t, code, 400)
	setIdentityActorEnforced(false)
	code, out, _ := e.do("POST", path, `{"content":"x"}`, nil)
	assert.Equal(t, 400, code)
	assert.Contains(t, out["error"], "verified sender")
	setIdentityActorEnforced(true)

	hdr := map[string]string{"X-Test-Actor": "token:Backend Dev"}
	code, _, _ = e.do("POST", path, `{"content":""}`, hdr)
	assert.Equal(t, 400, code)
	code, _, _ = e.do("POST", path, `not json`, hdr)
	assert.Equal(t, 400, code)
	code, _, _ = e.do("POST", path, `{"content":"  \u001b[31m  "}`, hdr)
	assert.Equal(t, 400, code, "text that is only terminal control is empty")
	assert.Empty(t, e.messages("p", "?task_id="+itoa(id)))
}

func TestAReplyHasTheCapOfAPost(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev", "Frontend Dev")
	id := e.task("p", "Backend Dev")
	hdr := map[string]string{"X-Test-Actor": "token:Frontend Dev"}
	path := "/api/board/p/tasks/" + itoa(id) + "/reply"
	big, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", maxBoardPostContent+1)})
	code, out, _ := e.do("POST", path, string(big), hdr)
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "content_too_large", out["code"])
	assert.Empty(t, e.messages("p", "?task_id="+itoa(id)))
	ok, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", maxBoardPostContent)})
	code, _, _ = e.do("POST", path, string(ok), hdr)
	assert.Equal(t, 200, code, "exactly the cap is fine: the creator's mention is not counted against it")
}

func TestTheTaskFilterOfTheMessageListIsValidatedAndScopedToTheBoard(t *testing.T) {
	e := newReplyEnv(t)
	e.join("p", "Backend Dev", "Frontend Dev")
	e.join("q", "Backend Dev")
	id := e.task("p", "Backend Dev")
	hdr := map[string]string{"X-Test-Actor": "token:Frontend Dev"}
	e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"one"}`, hdr)
	e.do("POST", "/api/board/p/tasks/"+itoa(id)+"/reply", `{"content":"two"}`, hdr)
	e.do("POST", "/api/board/p/messages", `{"subscriber_id":"Frontend Dev","content":"not a reply"}`, hdr)
	assert.Len(t, e.messages("p", "?task_id="+itoa(id)), 2)
	assert.Empty(t, e.messages("q", "?task_id="+itoa(id)), "another board's list never shows it")
	assert.Empty(t, e.messages("p", "?task_id=999"))
	for _, bad := range []string{"?task_id=", "?task_id=abc", "?task_id=0", "?task_id=-1", "?task_id=1;DROP", "?task_id=1.5"} {
		code, _, _ := e.do("GET", "/api/board/p/messages/all"+bad, "", nil)
		assert.Equal(t, 400, code, bad)
	}
	// the plain list still has every message, replies carrying their task id
	all := e.messages("p", "")
	assert.Len(t, all, 3)
}

// A reply is a post for an agent's write budget (Security's condition 5).
func TestARepliesCountAgainstTheAgentWriteBudget(t *testing.T) {
	assert.True(t, limitedAgentWriteOf(http.MethodPost, "/api/board/p/tasks/7/reply"))
	assert.True(t, limitedAgentWriteOf(http.MethodPost, "/api/board/p/messages"))
	assert.False(t, limitedAgentWriteOf(http.MethodGet, "/api/board/p/tasks/7/reply"))
	assert.False(t, limitedAgentWriteOf(http.MethodPost, "/api/board/p/tasks/7/reply/x"))
}

func limitedAgentWriteOf(method, path string) bool {
	return limitedAgentWrite(httptest.NewRequest(method, path, nil))
}

// #5779 (QA's #5774 e2e): the dashboard's reply was refused ("a reply needs a verified sender") in log mode, the default identity mode, because attributedTo gave back the empty
// claim of a body that has no sender; it worked only under enforcement, which is what every test here ran in. A reply is sent by whoever the request proves it is, in either mode:
// the verified Operator as "Operator", an agent's token as its member, Coral Hub's user as itself; a name in the body is never used; no verified actor is still refused.
func TestTheVerifiedActorRepliesInEitherIdentityMode(t *testing.T) {
	for _, enforced := range []bool{true, false} {
		mode := map[bool]string{true: "enforce", false: "log"}[enforced]
		t.Run(mode, func(t *testing.T) {
			e := newReplyEnv(t)
			setIdentityActorEnforced(enforced)
			e.join("p", "Backend Dev", "Frontend Dev")
			id := e.task("p", "Backend Dev")
			path := "/api/board/p/tasks/" + itoa(id) + "/reply"

			// the verified Operator (the dashboard) is the sender, whatever the body names
			code, out, b := e.do("POST", path, `{"content":"from the dashboard","subscriber_id":"Frontend Dev","as":"Backend Dev"}`, map[string]string{"X-Test-Actor": "operator"})
			require.Equal(t, 200, code, mode+": "+string(b))
			assert.Equal(t, []any{"Backend Dev"}, out["notified"])
			// an agent's token is its own member
			code, _, b = e.do("POST", path, `{"content":"from an agent","subscriber_id":"Backend Dev"}`, map[string]string{"X-Test-Actor": "token:Frontend Dev"})
			require.Equal(t, 200, code, mode+": "+string(b))
			senders := map[string]string{}
			for _, m := range e.messages("p", "?task_id="+itoa(id)) {
				senders[strings.TrimPrefix(m.Content, "@Backend Dev: ")] = m.SubscriberID
			}
			assert.Equal(t, map[string]string{"from the dashboard": "Operator", "from an agent": "Frontend Dev"}, senders, mode)

			// no verified actor: still refused, and nothing is posted
			code, _, _ = e.do("POST", path, `{"content":"nobody","subscriber_id":"Operator"}`, nil)
			assert.GreaterOrEqual(t, code, 400, mode)
			assert.Len(t, e.messages("p", "?task_id="+itoa(id)), 2, mode+": nothing from the unverified request")

			// Coral Hub's user replies as itself in either mode (its path never went through attributedTo)
			code, _, b = e.do("POST", path, `{"content":"from the hub"}`, hubReplyHdr("hub-user-1234abcd"))
			require.Equal(t, 200, code, mode+": "+string(b))
		})
	}
}
