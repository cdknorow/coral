package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// attributionHarness is the real identity layer (the observer, then the Operator-name guard) in
// front of the real handlers of the privileged routes (#4255): who may park and acknowledge, change
// the board's settings, and write the Operator's name, as each actor the layer can decide.
type attributionHarness struct {
	t      *testing.T
	h      http.Handler
	db     *store.DB
	bh     *BoardHandler
	tokens map[string]string
	cookie bool
	taskID int64
}

func newAttributionHarness(t *testing.T) *attributionHarness {
	t.Helper()
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	a := &attributionHarness{t: t, tokens: map[string]string{}}
	for _, s := range []struct{ key, id, title, session string }{
		{"orch", "Orchestrator", "Orchestrator", "claude-orch"}, {"dev", "Dev", "Developer", "claude-dev"}} {
		_, err := bh.bs.Subscribe(ctx, "team", s.id, s.title, s.session, nil, "all")
		require.NoError(t, err)
		a.tokens[s.key], err = db.MintSessionToken(ctx, "sid-"+s.key, s.session)
		require.NoError(t, err)
	}
	off := false // the cross-team switches default to on (#5916): these tests are about a board that has messaging off
	_, err = bh.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{CrossTeamMessaging: &off})
	require.NoError(t, err)
	task, err := bh.bs.CreateTask(ctx, "team", "T1", "", "medium", "Orchestrator", "Dev")
	require.NoError(t, err)
	a.taskID = task.ID

	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return a.cookie })
	r := chi.NewRouter()
	r.Use(o.Middleware)
	r.Use(RejectOperatorImpersonation)
	r.Post("/api/board/{project}/tasks/{taskID}/park", bh.ParkTask)
	r.Post("/api/board/{project}/health/ack", bh.AckHealthItem)
	r.Put("/api/board/{project}/settings", bh.UpdateBoardSettings)
	r.Post("/api/board/{project}/messages", bh.PostMessage)
	r.Get("/api/board/{project}/messages", bh.ReadMessages)
	r.Get("/api/board/{project}/messages/check", bh.CheckUnread)
	r.Post("/api/board/{project}/messages/mark-read", bh.MarkRead)
	r.Post("/api/board/{project}/subscribe", bh.Subscribe)
	r.Delete("/api/board/{project}/subscribe", bh.Unsubscribe)
	r.Get("/api/board/{project}/peek", bh.PeekAgent)
	r.Post("/api/board/{project}/waits", bh.RegisterWait)
	r.Get("/api/board/{project}/waits", bh.GetActiveWait)
	r.Delete("/api/board/{project}/waits", bh.CancelWait)
	r.Post("/api/board/{project}/reminder", bh.RemindSubscriber)
	r.Delete("/api/board/{project}/reminder", bh.RemindSubscriber)
	// The task verbs (#4255 commit 3), the real handlers behind the real identity layer.
	r.Post("/api/board/{project}/tasks", bh.CreateTask)
	r.Post("/api/board/{project}/tasks/claim", bh.ClaimTask)
	r.Post("/api/board/{project}/tasks/current", bh.ActiveTask)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", bh.CompleteTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/submit-review", bh.SubmitCompletionReview)
	r.Post("/api/board/{project}/tasks/{taskID}/release-review", bh.ReleaseCompletionReview)
	r.Post("/api/board/{project}/tasks/{taskID}/review", bh.DelegateReview)
	r.Post("/api/board/{project}/tasks/{taskID}/approve", bh.ApproveReview)
	r.Post("/api/board/{project}/tasks/{taskID}/reject", bh.RejectReview)
	r.Post("/api/board/{project}/tasks/{taskID}/cancel", bh.CancelTaskByID)
	r.Patch("/api/board/{project}/tasks/{taskID}", bh.UpdateTask)
	r.Patch("/api/board/{project}/tasks/{taskID}/amend", bh.AmendTask)
	r.Post("/api/board/{project}/tasks/{taskID}/reassign", bh.ReassignTask)
	r.Post("/api/board/{project}/tasks/{taskID}/publish", bh.PublishTask)
	r.Post("/api/board/{project}/tasks/{taskID}/nudge", bh.NudgeTask)
	r.Post("/api/board/{project}/tasks/{taskID}/snooze", bh.SnoozeTaskReminder)
	r.Post("/api/board/{project}/tasks/{taskID}/reminder", bh.RemindTask)
	r.Delete("/api/board/{project}/tasks/{taskID}/reminder", bh.RemindTask)
	// The Operator-only routes and the cross-team relay (#4255 commit 4).
	r.Post("/api/board/{project}/pause", bh.PauseBoard)
	r.Post("/api/board/{project}/resume", bh.ResumeBoard)
	r.Put("/api/board/{project}/working-mode", bh.SetWorkingMode)
	r.Post("/api/board/{project}/working-mode/presets", bh.SaveWorkflowPreset)
	r.Post("/api/board/{project}/relay-messages", bh.PostRelayMessage)
	r.Get("/api/board/{project}/relay-targets", bh.ListRelayTargets)
	a.bh = bh
	a.db = db
	// A reminder a test sets runs a goroutine of its own: stop them all before the leak check (#4167).
	t.Cleanup(func() {
		for _, id := range []string{"Orchestrator", "Dev", "Operator", "New One"} {
			bh.stopSubscriberReminder("team", id)
		}
	})
	a.h = r
	return a
}

// call sends one request as actor: "orch" or "dev" (their agent tokens), "operator" (the dashboard's
// cookie), or "claimed" (no credential at all, on an install that opted out of strict local auth).
func (a *attributionHarness) call(actor, method, path, body string) int {
	a.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	a.cookie = actor == "operator"
	if tok, ok := a.tokens[actor]; ok {
		r.Header.Set(store.SessionTokenHeader, tok)
		r.Header.Set("User-Agent", "coral-board/test")
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, r)
	return rec.Code
}

// callBody is call, with the response body.
func (a *attributionHarness) callBody(actor, method, path, body string) (int, string) {
	a.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	a.cookie = actor == "operator"
	if tok, ok := a.tokens[actor]; ok {
		r.Header.Set(store.SessionTokenHeader, tok)
		r.Header.Set("User-Agent", "coral-board/test")
	}
	if actor == "browser" { // this computer's browser with no sign-in: claimed, and a person to humanInputCaller
		r.Header.Set("Origin", "http://example.com") // the harness requests are to example.com
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// mintToken mints an agent token for a session on no board (a joining agent) in the harness's store.
func (a *attributionHarness) mintToken(t *testing.T, key, sid, session string) string {
	t.Helper()
	tok, err := a.db.MintSessionToken(context.Background(), sid, session)
	require.NoError(t, err)
	a.tokens[key] = tok
	return key
}

// callBodyWithToken is callBody for a token minted with mintToken.
func (a *attributionHarness) callBodyWithToken(key, method, path, body string) (int, string) {
	return a.callBody(key, method, path, body)
}

func setMode(t *testing.T, enforce bool) {
	t.Helper()
	was := identityActorEnforced()
	setIdentityActorEnforced(enforce)
	resetEnforceDiffers()
	t.Cleanup(func() { setIdentityActorEnforced(was); resetEnforceDiffers() })
}

// Who may park, acknowledge, change the board's settings and write the Operator's name, by actor, in
// both modes. The orchestrator is the Operator's only agent. A claimed actor (no credential) is let
// through in log mode, as before, and counted; under enforce it is refused. The orchestrator and a
// member never differ between the modes.
func TestPrivilegedRoutesDecideByTheActor(t *testing.T) {
	park := func(a *attributionHarness) (string, string, string) {
		return "POST", fmt.Sprintf("/api/board/team/tasks/%d/park", a.taskID), `{"reason":"waiting"}`
	}
	ack := func(a *attributionHarness) (string, string, string) {
		return "POST", "/api/board/team/health/ack", `{"item":"0123abcd"}`
	}
	settings := func(a *attributionHarness) (string, string, string) {
		return "PUT", "/api/board/team/settings", `{"subscriber_id":"Operator","health":{"on":true}}`
	}
	settingsNoClaim := func(a *attributionHarness) (string, string, string) {
		return "PUT", "/api/board/team/settings", `{"health":{"on":true}}`
	}
	operatorName := func(a *attributionHarness) (string, string, string) {
		return "POST", "/api/board/team/messages", `{"subscriber_id":"Operator","content":"hi"}`
	}
	type want struct{ log, enforce bool } // the request was let through (not refused)
	cases := []struct {
		name  string
		build func(*attributionHarness) (string, string, string)
		site  string
		who   map[string]want
	}{
		{"park", park, "health-admin POST /api/board/{project}/tasks/{taskID}/park",
			map[string]want{"orch": {true, true}, "dev": {false, false}, "operator": {true, true}, "claimed": {true, false}}},
		{"ack", ack, "health-admin POST /api/board/{project}/health/ack",
			map[string]want{"orch": {true, true}, "dev": {false, false}, "operator": {true, true}, "claimed": {true, false}}},
		{"settings (claims the Operator)", settings, "board-settings PUT /api/board/{project}/settings",
			map[string]want{"orch": {false, false}, "dev": {false, false}, "operator": {true, true}, "claimed": {true, false}}},
		{"settings (no claim: the claim is optional under enforce)", settingsNoClaim, "board-settings PUT /api/board/{project}/settings",
			map[string]want{"orch": {false, false}, "dev": {false, false}, "operator": {false, true}, "claimed": {false, false}}},
		{"the Operator's name in a post", operatorName, "operator-name POST /api/board/{project}/messages",
			map[string]want{"orch": {false, false}, "dev": {false, false}, "operator": {true, true}, "claimed": {true, false}}},
	}
	for _, mode := range []string{"log", "enforce"} {
		for _, c := range cases {
			for actor, w := range c.who {
				t.Run(mode+"/"+c.name+"/"+actor, func(t *testing.T) {
					a := newAttributionHarness(t)
					setMode(t, mode == "enforce")
					method, path, body := c.build(a)
					code := a.call(actor, method, path, body)
					// A refusal is a 403 (or the Operator-name guard's 400); an allowed ack of an item that does not exist is a 404.
					letThrough := code != http.StatusForbidden && code != http.StatusBadRequest
					expect := w.log
					if mode == "enforce" {
						expect = w.enforce
					}
					assert.Equal(t, expect, letThrough, "%s as %s in %s mode answered %d", c.name, actor, mode, code)
					if mode == "log" && w.log != w.enforce {
						assert.Contains(t, enforceDiffersSites(), c.site, "log mode counts what enforce would decide differently")
					}
					if mode == "log" && w.log == w.enforce {
						assert.NotContains(t, enforceDiffersSites(), c.site, "an actor the modes agree on is not counted")
					}
				})
			}
		}
	}
}

// claimIdentity records how a creator's or amender's name was backed, from the request's actor, not
// from the claim (#4255): rule 13 trusts a task's named reviewers by it.
func TestClaimIdentityComesFromTheActor(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	hubUser := "hub-user-1a2b3c4d"
	cases := []struct {
		name    string
		actor   *Actor // nil: a request that did not pass the identity layer
		claimed string
		want    string
	}{
		{"a member, its own name", &Actor{Kind: ActorToken, Name: "Dev", Session: "s"}, "Dev", "token"},
		{"a member, no claim: the claim is optional", &Actor{Kind: ActorToken, Name: "Dev", Session: "s"}, "", "token"},
		{"a member, its name in another case", &Actor{Kind: ActorToken, Name: "Dev", Session: "s"}, "dev", "token"},
		{"a member claiming another member", &Actor{Kind: ActorToken, Name: "Dev", Session: "s"}, "Lead", "claimed"},
		{"a member claiming the Operator", &Actor{Kind: ActorToken, Name: "Dev", Session: "s"}, "Operator", "claimed"},
		{"a joining token (no name yet)", &Actor{Kind: ActorToken, Session: "s"}, "Dev", "claimed"},
		{"the Operator, the name it is given", &Actor{Kind: ActorOperator, Name: "Operator"}, "Operator", "operator"},
		{"the Operator, lower case (the signed command line sends it)", &Actor{Kind: ActorOperator, Name: "Operator"}, "operator", "operator"},
		{"the Operator, no claim", &Actor{Kind: ActorOperator, Name: "Operator"}, "", "operator"},
		{"the Operator claiming a member's name", &Actor{Kind: ActorOperator, Name: "Operator"}, "Dev", "claimed"},
		{"the Hub operator", &Actor{Kind: ActorHub, Name: "Operator", HubUser: hubUser}, "Operator", "operator"},
		{"a Hub user", &Actor{Kind: ActorHub, Name: hubUser, HubUser: hubUser}, "Operator", "claimed"},
		{"no credential, claiming the Operator", &Actor{Kind: ActorClaimed}, "Operator", "claimed"},
		{"no credential, claiming the orchestrator", &Actor{Kind: ActorClaimed}, "Orchestrator", "claimed"},
		{"no identity layer", nil, "Operator", "claimed"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/board/team/tasks", nil)
		if c.actor != nil {
			r = withActor(r, *c.actor)
		}
		assert.Equal(t, c.want, bh.claimIdentity(r, "team", c.claimed), c.name)
	}
}
