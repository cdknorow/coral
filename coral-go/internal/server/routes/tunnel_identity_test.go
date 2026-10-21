package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// stamp marks a request as executed by this process's Coral Hub connector.
func stamp(r *http.Request) *http.Request {
	r.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	return r
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	s, _ := out["code"].(string)
	return s
}

// #3377 (SEC-BD-02): through the tunnel nobody acts with an orchestrator's rights. #3404:
// the Operator may be used (by default; refused with the owner's opt-out), only with a
// valid Coral Hub user, and every use is recorded with that user. Requests from this
// computer are unchanged.
func TestTunnelIdentityGuard(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := handler.bs.Subscribe(ctx, "p", "Lead", "Developer", "s1", nil, "", true) // can_peek
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, "p", "Dev", "Developer", "s2", nil, "")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, "p", "hub-user-0000beef", "Developer", "s3", nil, "")
	require.NoError(t, err) // a member created before the name was reserved

	operatorOK := true
	passed, seenHubUser := 0, ""
	g := TunnelIdentityGuard(handler.bs, func(*http.Request) bool { return operatorOK })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed++
		seenHubUser = hubOperatorUser(r)
		if strings.HasSuffix(r.URL.Path, "/fails") {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	const noHeader = "\x00none"
	do := func(tunneled bool, method, target, body string, hubUsers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if tunneled {
			stamp(r)
		}
		for _, u := range hubUsers {
			if u != noHeader {
				r.Header.Add(tunnel.HubUserHeader, u)
			}
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, r)
		return rec
	}

	// Orchestrator self-promotion and impersonation: refused, also with the Operator allowed
	// and a valid Hub user.
	for _, tc := range []struct{ target, body string }{
		{"/api/board/p/subscribe", `{"subscriber_id":"w","job_title":"Orchestrator"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"my-orchestrator"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"w","receive_mode":"all"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"Operator","receive_mode":"all"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"Operator","job_title":"Orchestrator"}`},
		{"/api/board/p/tasks/claim", `{"subscriber_id":"Lead"}`}, // an existing peek-capable subscriber
		{"/api/board/p/tasks", `{"created_by":"Orchestrator","title":"x"}`},
		{"/api/board/p/tasks", `{"subscriber_id":"Operator","created_by":"Orchestrator","title":"x"}`},
	} {
		rec := do(true, "POST", tc.target, tc.body, "hub-user-1234abcd")
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.body)
		assert.Equal(t, "orchestrator_identity", codeOf(t, rec), tc.body)
	}

	operatorActions := []struct{ method, target, body string }{
		{"POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`},
		{"PATCH", "/api/board/p/tasks/1", `{"assigned_to":"operator"}`},
		{"PATCH", "/api/board/p/tasks/1/amend", `{"subscriber_id":"Operator","revision":1,"reason":"r"}`},
		{"POST", "/api/board/p/tasks/1/reassign", `{"subscriber_id":"Operator","assignee":"Dev"}`},
		{"POST", "/api/board/p/tasks/1/complete", `{"subscriber_id":"Operator"}`},
		{"POST", "/api/board/q/external-messages", `{"from_board":"p","subscriber_id":"Operator","content":"x"}`},
		{"DELETE", "/api/board/p/waits?subscriber_id=Operator", ``},
	}

	// By default the Operator is allowed with a valid Hub user; the handler sees who.
	for _, tc := range operatorActions {
		before := passed
		seenHubUser = ""
		rec := do(true, tc.method, tc.target, tc.body, "hub-user-1234abcd")
		assert.Equal(t, http.StatusOK, rec.Code, tc.body)
		assert.Equal(t, before+1, passed, tc.body)
		assert.Equal(t, "hub-user-1234abcd", seenHubUser, tc.body)
	}
	// Recorded with the Hub user and the outcome (the newest first).
	assert.Equal(t, http.StatusNotFound, do(true, "POST", "/api/board/p/tasks/9/fails", `{"subscriber_id":"Operator"}`, "hub-user-1234abcd").Code)
	recent, err := handler.bs.RecentHubOperatorActions(ctx, 100)
	require.NoError(t, err)
	require.Len(t, recent, len(operatorActions)+1)
	assert.Equal(t, board.HubOperatorAction{ID: recent[0].ID, At: recent[0].At, HubUser: "hub-user-1234abcd", Method: "POST", Path: "/api/board/p/tasks/9/fails", Project: "p", Status: http.StatusNotFound}, recent[0])
	assert.Equal(t, "PATCH", recent[len(recent)-3].Method)
	assert.Equal(t, http.StatusOK, recent[len(recent)-1].Status)

	// Never the Operator without a valid Hub user: none (an older hub) 403, malformed or
	// repeated 400, a name already on the board 409. Nothing reaches the handler.
	before := passed
	for _, tc := range []struct {
		users []string
		code  int
	}{
		{[]string{noHeader}, http.StatusForbidden},
		{[]string{""}, http.StatusBadRequest},
		{[]string{"Operator"}, http.StatusBadRequest},
		{[]string{"hub-user-XYZ"}, http.StatusBadRequest},
		{[]string{"hub-user-1234abcd, hub-user-5678abcd"}, http.StatusBadRequest},
		{[]string{"hub-user-1234abcd", "hub-user-1234abcd"}, http.StatusBadRequest},
		{[]string{"hub-user-1234abcd", "hub-user-5678abcd"}, http.StatusBadRequest},
		{[]string{"hub-user-0000beef"}, http.StatusConflict},
	} {
		rec := do(true, "POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`, tc.users...)
		assert.Equal(t, tc.code, rec.Code, tc.users)
		assert.Equal(t, "hub_user_required", codeOf(t, rec), tc.users)
	}
	assert.Equal(t, before, passed)
	recent, _ = handler.bs.RecentHubOperatorActions(ctx, 100)
	assert.Len(t, recent, len(operatorActions)+1, "refusals are not Operator actions")

	// With the owner's opt-out: refused in any identity field, whoever the Hub user is.
	operatorOK = false
	for _, tc := range operatorActions {
		rec := do(true, tc.method, tc.target, tc.body, "hub-user-1234abcd")
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.body)
		assert.Equal(t, "operator_identity_required", codeOf(t, rec), tc.body)
	}
	assert.Equal(t, before, passed)

	// A member's name through the tunnel is refused like a chat post's (#3428): a Coral Hub
	// user can't claim, complete, rename, unsubscribe or wait AS an existing member, with
	// the opt-out on or off; any other made-up name is refused too. Nothing reaches the handler.
	for _, optOutOn := range []bool{false, true} {
		operatorOK = !optOutOn
		before = passed
		for _, tc := range []struct{ method, target, body string }{
			{"POST", "/api/board/p/tasks/claim", `{"subscriber_id":"Dev"}`},
			{"POST", "/api/board/p/tasks/1/complete", `{"subscriber_id":"dev ","message":"x"}`},
			{"POST", "/api/board/p/subscribe", `{"subscriber_id":"Dev","job_title":"Hacked"}`},
			{"DELETE", "/api/board/p/subscribe", `{"subscriber_id":"Dev\u00a0"}`},
			{"POST", "/api/board/p/waits", `{"subscriber_id":"Dev","wait_type":"message","target_id":"x"}`},
			{"POST", "/api/board/p/subscribe", `{"subscriber_id":"Developer"}`}, // an existing ROLE
			{"POST", "/api/board/p/tasks", `{"created_by":"Dev","title":"x"}`},
		} {
			rec := do(true, tc.method, tc.target, tc.body, "hub-user-1234abcd")
			assert.Equal(t, http.StatusConflict, rec.Code, tc.body)
			assert.Equal(t, "member_identity", codeOf(t, rec), tc.body)
		}
		if !optOutOn { // the Operator claiming an existing role is refused too
			rec := do(true, "POST", "/api/board/p/subscribe", `{"subscriber_id":"Operator","job_title":"Developer"}`, "hub-user-1234abcd")
			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Equal(t, "member_identity", codeOf(t, rec))
		}
		rec := do(true, "POST", "/api/board/p/tasks/claim", `{"subscriber_id":"Mallory"}`, "hub-user-1234abcd")
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "hub_identity_required", codeOf(t, rec))
		assert.Equal(t, before, passed)
	}
	// A write that names no actor (pause, a reminder) is the Operator's: allowed and recorded
	// by default, refused with the opt-out.
	operatorOK = true
	n0 := len(mustRecent(t, handler, ctx))
	before = passed
	seenHubUser = ""
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/pause", `{}`, "hub-user-1234abcd").Code)
	assert.Equal(t, "hub-user-1234abcd", seenHubUser)
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/reminder", `{"subscriber_id":"Dev","message":"hi"}`, "hub-user-1234abcd").Code, "the reminded subscriber is a target, not an actor")
	assert.Equal(t, before+2, passed)
	assert.Len(t, mustRecent(t, handler, ctx), n0+2, "every board write through the tunnel is recorded")
	operatorOK = false
	assert.Equal(t, http.StatusForbidden, do(true, "POST", "/api/board/p/pause", `{}`, "hub-user-1234abcd").Code)
	assert.Equal(t, http.StatusForbidden, do(true, "POST", "/api/board/p/reminder", `{"subscriber_id":"Dev"}`, "hub-user-1234abcd").Code)
	assert.Equal(t, before+2, passed)
	operatorOK = true

	// Unchanged: reads, chat posts (PostMessage decides their sender), and everything from
	// this computer.
	before = passed
	assert.Equal(t, http.StatusOK, do(true, "GET", "/api/board/p/messages?subscriber_id=Operator", ``).Code)
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/messages", `{"subscriber_id":"Operator","content":"hi"}`).Code)
	assert.Equal(t, http.StatusOK, do(false, "POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`).Code)
	assert.Equal(t, "", seenHubUser, "from this computer it is the local Operator")
	assert.Equal(t, http.StatusOK, do(false, "POST", "/api/board/p/subscribe", `{"subscriber_id":"Orchestrator","receive_mode":"all"}`).Code)
	assert.Equal(t, before+4, passed)
}

// With the opt-out on (here: the handler has no Hub-as-Operator check, which means never),
// a tunnel chat post is sent as the Coral Hub user from the header, marked origin
// hub_user; never the Operator, never a member's name. The header means nothing locally.
func TestTunnelChatPostIsTheHubUser(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	_, err := handler.bs.Subscribe(context.Background(), "p", "hub-user-0000beef", "Developer", "s3", nil, "")
	require.NoError(t, err) // a member created before the name was reserved
	const noHeader = "\x00none"
	post := func(tunneled bool, hubUser, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", server.URL+"/api/board/p/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if tunneled {
			stamp(req)
		}
		if hubUser != noHeader {
			req.Header.Set(tunnel.HubUserHeader, hubUser)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	body := `{"subscriber_id":"Operator","as":"Operator","content":"hello from the hub"}`
	for _, bad := range []string{"", "hub-user-XYZ", "Operator", "@Lead", "hub-user-1234567890", "hub:1234abcd"} { // present but malformed
		code, out := post(true, bad, body)
		assert.Equal(t, http.StatusBadRequest, code, bad)
		assert.Equal(t, "hub_user_required", out["code"], bad)
	}
	code, _ := post(true, "hub-user-0000beef", body)
	assert.Equal(t, http.StatusConflict, code, "already a member's name")

	code, out := post(true, "hub-user-1234abcd", body)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hub-user-1234abcd", out["subscriber_id"], "the Hub user, not the body's Operator")
	assert.Equal(t, board.OriginHubUser, out["origin"])
	sub, _ := handler.bs.GetProjectSubscription(context.Background(), "p", "Operator")
	assert.Nil(t, sub, "no auto-subscribe through the tunnel")

	// Stored and listed with its origin.
	resp, err := http.Get(server.URL + "/api/board/p/messages/all")
	require.NoError(t, err)
	var all []board.Message
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&all))
	resp.Body.Close()
	require.NotEmpty(t, all)
	last := all[len(all)-1]
	require.NotNil(t, last.Origin)
	assert.Equal(t, board.OriginHubUser, *last.Origin)

	// A post with no header at all (a hub on an older pkg/hubproxy, accepted as 'Coral Hub' for
	// one release) is refused (#3743), and never the body's Operator or any member.
	code, out = post(true, noHeader, `{"subscriber_id":"Operator","as":"Operator","content":"from an old hub"}`)
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "hub_user_required", out["code"])
	sub, _ = handler.bs.GetProjectSubscription(context.Background(), "p", "Operator")
	assert.Nil(t, sub)
	assert.True(t, board.IsReservedName("Coral Hub"), "no member can take that name")

	// From this computer the header is ignored: the body's sender, no origin.
	code, out = post(false, "hub-user-1234abcd", `{"subscriber_id":"Operator","content":"local"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Operator", out["subscriber_id"])
	assert.Nil(t, out["origin"])
	// A local client can't set origin either (it isn't read from the body).
	code, out = post(false, noHeader, `{"subscriber_id":"Dev","content":"x","origin":"hub_user"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, out["origin"])
}

func hubTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
	return dir
}

// #3377 (SEC-JB-01): through the tunnel an auto-accepting run (the flag, or CLI flags that skip
// permission prompts in any spelling, agent.SkipsPermissionPrompts, #126 F4) needs the local allow-hub-auto-accept switch and Launch mode; repo_path
// must be a git work tree for everyone.
func TestTasksRunAutoAcceptThroughTheHub(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewTasksHandler(db, &config.Config{})
	ss := store.NewSessionStore(db)
	repo := hubTestGitRepo(t)
	run := func(tunneled, authentic bool, body string) (int, string) {
		r := httptest.NewRequest("POST", "/api/tasks/run", strings.NewReader(body))
		if tunneled {
			r.Header.Set(tunnel.ViaHeader, "forged")
			if authentic {
				stamp(r)
			}
		}
		rec := httptest.NewRecorder()
		h.SubmitTask(rec, r)
		return rec.Code, codeOf(t, rec)
	}
	auto := `{"prompt":"p","repo_path":` + jsonString(repo) + `,"auto_accept":true}`
	flags := `{"prompt":"p","repo_path":` + jsonString(repo) + `,"flags":"--dangerously-skip-permissions"}`
	// Spellings the old substring list missed (#126 F4).
	codexNever := `{"prompt":"p","repo_path":` + jsonString(repo) + `,"agent_type":"codex","flags":"-a never"}`
	claudeEdits := `{"prompt":"p","repo_path":` + jsonString(repo) + `,"flags":"--permission-mode acceptEdits"}`
	geminiYolo := `{"prompt":"p","repo_path":` + jsonString(repo) + `,"agent_type":"gemini","flags":"--approval-mode yolo"}`

	code, c := run(true, true, auto)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_auto_accept_disabled", c, "the switch is off unless the owner turned it on (#126 F4)")
	require.NoError(t, ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "true"))
	code, c = run(true, true, auto)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_mode_too_low", c, "max mode defaults to view")
	require.NoError(t, ss.SetSetting(context.Background(), MaxModeSetting, "launch"))
	code, _ = run(true, true, auto)
	assert.Equal(t, http.StatusOK, code, "Launch mode, switch on")
	code, c = run(true, false, auto)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "no_dashboard_provenance", c, "a forged stamp")
	require.NoError(t, ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "false"))
	for _, b := range []string{auto, flags, codexNever, claudeEdits, geminiYolo} {
		code, c = run(true, true, b)
		assert.Equal(t, http.StatusForbidden, code, b)
		assert.Equal(t, "hub_auto_accept_disabled", c, b)
	}
	code, _ = run(false, false, auto)
	assert.Equal(t, http.StatusOK, code, "from this computer: unchanged")
	code, _ = run(true, true, `{"prompt":"p","repo_path":`+jsonString(repo)+`}`)
	assert.Equal(t, http.StatusOK, code, "no auto-accept: no switch needed")
	code, _ = run(true, true, `{"prompt":"p","repo_path":`+jsonString(repo)+`,"agent_type":"codex","flags":"--sandbox read-only -a on-request"}`)
	assert.Equal(t, http.StatusOK, code, "flags that keep the prompts: no switch needed")

	notGit := t.TempDir()
	code, _ = run(false, false, `{"prompt":"p","repo_path":`+jsonString(notGit)+`}`)
	assert.Equal(t, http.StatusBadRequest, code, "repo_path must be a git work tree")
	_ = os.RemoveAll(notGit)
}

// #3404: by default a Coral Hub user's chat post that asks to be the Operator is sent as
// the Operator, attributed to the Hub user (origin hub_operator, hub_user), and its text says
// so, also to an agent reading it. Without a Hub user it is never the Operator.
func TestTunnelChatPostAsTheOperator(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := handler.bs.Subscribe(ctx, "p", "Dev", "Backend Dev", "s2", nil, "")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, "p", "hub-user-0000beef", "Developer", "s3", nil, "")
	require.NoError(t, err)
	allowed := true
	handler.SetHubOperatorAllowed(func(r *http.Request) bool { return allowed && cloud.TunneledAuthentic(r) })
	const noHeader = "\x00none"
	post := func(hubUsers []string, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", server.URL+"/api/board/p/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		stamp(req)
		for _, u := range hubUsers {
			if u != noHeader {
				req.Header.Add(tunnel.HubUserHeader, u)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	one := func(u string) []string { return []string{u} }
	asOperator := `{"subscriber_id":"Operator","as":"Operator","content":"@Dev please look at the build"}`

	code, out := post(one("hub-user-1234abcd"), asOperator)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "Operator", out["subscriber_id"])
	assert.Equal(t, board.OriginHubOperator, out["origin"])
	assert.Equal(t, "hub-user-1234abcd", out["hub_user"])
	assert.Equal(t, "[Operator via Coral Hub, hub-user-1234abcd] @Dev please look at the build", out["content"])
	sub, _ := handler.bs.GetProjectSubscription(ctx, "p", "Operator")
	assert.Nil(t, sub, "the Operator's subscription is the local dashboard's")
	recent, err := handler.bs.RecentHubOperatorActions(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Equal(t, "hub-user-1234abcd", recent[0].HubUser)
	assert.Equal(t, "/api/board/p/messages", recent[0].Path)

	// What the agent reads (coral-board read) says who it is from.
	resp, err := http.Get(server.URL + "/api/board/p/messages?subscriber_id=Dev")
	require.NoError(t, err)
	var read []board.Message
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&read))
	resp.Body.Close()
	require.Len(t, read, 1)
	assert.Equal(t, "Operator", read[0].SubscriberID)
	assert.True(t, strings.HasPrefix(read[0].Content, "[Operator via Coral Hub, hub-user-1234abcd] "), read[0].Content)
	require.NotNil(t, read[0].HubUser)
	assert.Equal(t, "hub-user-1234abcd", *read[0].HubUser)

	// Not asking to be the Operator: the Hub user, as in v1.4.18, also recorded.
	code, out = post(one("hub-user-1234abcd"), `{"subscriber_id":"Dev","content":"hi"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hub-user-1234abcd", out["subscriber_id"])
	assert.Equal(t, board.OriginHubUser, out["origin"])
	assert.Equal(t, "hub-user-1234abcd", out["hub_user"])
	assert.Equal(t, "hi", out["content"])

	// Never the Operator without a valid Hub user.
	code, out = post(one(noHeader), asOperator)
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "hub_user_required", out["code"])
	for _, bad := range [][]string{one(""), one("Operator"), one("hub-user-XYZ"), {"hub-user-1234abcd", "hub-user-1234abcd"}, {"hub-user-1234abcd", "hub-user-5678abcd"}} {
		code, out = post(bad, asOperator)
		assert.Equal(t, http.StatusBadRequest, code, bad)
		assert.Equal(t, "hub_user_required", out["code"], bad)
	}
	code, _ = post(one("hub-user-0000beef"), asOperator)
	assert.Equal(t, http.StatusConflict, code)

	// The opt-out: the Hub user again, not the Operator.
	allowed = false
	code, out = post(one("hub-user-1234abcd"), asOperator)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hub-user-1234abcd", out["subscriber_id"])
	assert.Equal(t, "@Dev please look at the build", out["content"])
	recent, _ = handler.bs.RecentHubOperatorActions(ctx, 10)
	assert.Len(t, recent, 3, "every board write through the tunnel is recorded (#3428): the Operator post, the Hub user's and the post under the opt-out (the one with no Hub user was refused)")

	// A forged stamp (not this connector's) is never the Operator either.
	allowed = true
	req, _ := http.NewRequest("POST", server.URL+"/api/board/p/messages", bytes.NewBufferString(asOperator))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.ViaHeader, "forged")
	req.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	var forged map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&forged)
	resp.Body.Close()
	assert.Equal(t, "hub-user-1234abcd", forged["subscriber_id"])

	// Old rows: a Hub user's earlier posts get hub_user from their sender (migration).
	// (Covered in internal/board; here the new column is listed by the dashboard's read.)
	resp, err = http.Get(server.URL + "/api/board/p/messages/all")
	require.NoError(t, err)
	var all []board.Message
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&all))
	resp.Body.Close()
	require.NotEmpty(t, all)
	require.NotNil(t, all[0].HubUser)
	assert.Equal(t, "Operator", all[0].SubscriberID)
}

// #3404: a cross-team post by the Operator through Coral Hub says so in its text, so the
// receiving Orchestrator can tell; without the guard's Hub user it is refused.
func TestCrossTeamOperatorThroughTheHub(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("alpha", true)
	e.enable("beta", true)
	send := func(hubUser string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/board/beta/external-messages", strings.NewReader(`{"from_board":"alpha","subscriber_id":"Operator","content":"status?"}`))
		stamp(r)
		if hubUser != "" {
			r = withHubOperator(r, hubUser)
		}
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "beta")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		e.h.PostExternalMessage(rec, r)
		return rec
	}
	rec := send("")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "operator_identity_required", codeOf(t, rec))
	rec = send("hub-user-1234abcd")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "@Orchestrator [Operator via Coral Hub, hub-user-1234abcd] status?", out.Message.Content)
	// No rate-limit exemption: the Hub's Operator shares the Operator's cross-team limit.
	limited := false
	for i := 0; i < crossTeamBurst+1 && !limited; i++ {
		limited = send("hub-user-1234abcd").Code == http.StatusTooManyRequests
	}
	assert.True(t, limited, "the cross-team limit applies through Coral Hub")
	e.now = e.now.Add(time.Hour)
	// From this computer: unchanged.
	code, body := e.send("alpha", "Operator", "", "local status?")
	require.Equal(t, http.StatusOK, code, body)
	msg, _ := body["message"].(map[string]any)
	assert.Equal(t, "@Orchestrator local status?", msg["content"])
}

// #3404: "Don't let Coral Hub act as the Operator" is an opt-out, off by default; set only
// from the dashboard on this computer (not with PUT /api/settings), readable but not
// changeable through the tunnel, honoured only on an authentic tunnel request while linked.
func TestHubOperatorSetting(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	h := NewCloudHandler(ss, t.TempDir())
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "b.db"))
	require.NoError(t, err)
	defer bs.Close()
	h.SetBoardStore(bs)
	linked := false
	h.linked = func() bool { return linked }
	// The request below has no real socket, so the kernel can't name its user (peercred
	// fails closed, other_user): say it's Coral's own user, as a real loopback
	// request from this process is (peercred's TestSameUserRealRequest) (#3442).
	peer := [2]bool{true, true}
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return peer[0], peer[1] })
	local := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8420/api/system/hub-operator", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:8420") // the dashboard's own page
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		h.HubOperator(rec, r)
		return rec
	}
	tunneled := func(method, body string) *httptest.ResponseRecorder {
		r := stamp(httptest.NewRequest(method, "/api/system/hub-operator", strings.NewReader(body)))
		rec := httptest.NewRecorder()
		h.HubOperator(rec, r)
		return rec
	}
	authentic := func() *http.Request { return stamp(httptest.NewRequest("POST", "/api/board/p/tasks", nil)) }

	rec := local("GET", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"disabled":false,"linked":false,"recent_actions":[]}`, rec.Body.String())
	// Another OS user on this computer can neither read nor change it.
	peer = [2]bool{false, true}
	for _, m := range []string{"GET", "PUT"} {
		rec := local(m, `{"disabled":true}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, m)
		assert.Contains(t, rec.Body.String(), `"other_user"`, m)
	}
	peer = [2]bool{false, false} // a system peercred can't ask: the dashboard rules alone
	require.Equal(t, http.StatusOK, local("GET", "").Code)
	peer = [2]bool{true, true}
	assert.JSONEq(t, `{"disabled":false}`, tunneled("GET", "").Body.String(), "the hub can read it")

	// Honoured only while linked, and only on an authentic tunnel request.
	assert.False(t, h.HubOperatorAllowed(authentic()), "not linked")
	linked = true
	assert.True(t, h.HubOperatorAllowed(authentic()), "linked, opt-out off: the default")
	assert.False(t, h.HubOperatorAllowed(httptest.NewRequest("POST", "/api/board/p/tasks", nil)), "not through the tunnel")
	forged := httptest.NewRequest("POST", "/api/board/p/tasks", nil)
	forged.Header.Set(tunnel.ViaHeader, "1")
	assert.False(t, h.HubOperatorAllowed(forged), "a stamp that isn't this connector's")

	// The opt-out.
	rec = local("PUT", `{"disabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"disabled":true,"linked":true,"recent_actions":[]}`, rec.Body.String())
	assert.False(t, h.HubOperatorAllowed(authentic()))
	assert.JSONEq(t, `{"disabled":true}`, tunneled("GET", "").Body.String())
	require.Equal(t, http.StatusOK, local("PUT", `{"disabled":false}`).Code)
	assert.True(t, h.HubOperatorAllowed(authentic()))
	require.Equal(t, http.StatusOK, local("PUT", `{"disabled":true}`).Code)
	assert.False(t, h.HubOperatorAllowed(authentic()))
	// v1.4.18's {"enabled": ...} is no longer read (#3743): only "disabled" sets it.
	for _, bad := range []string{`{}`, `{"enabled":true}`, `{"enabled":false}`, `{"disabled":"yes"}`} {
		assert.Equal(t, http.StatusBadRequest, local("PUT", bad).Code, bad)
	}

	// Never changed through the tunnel (and never routed there: PUT is TierNever).
	for _, m := range []string{"PUT", "POST", "DELETE"} {
		assert.Equal(t, http.StatusNotFound, tunneled(m, `{"disabled":false}`).Code, m)
	}
	assert.Equal(t, tunnel.TierView, tunnel.Classify("GET", "/api/system/hub-operator", ""))
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/system/hub-operator", ""))
	// View can't post; board settings are the owner's alone through the tunnel (#5307).
	assert.Equal(t, tunnel.TierInteract, tunnel.Classify("POST", "/api/board/p/messages", ""))
	assert.Equal(t, tunnel.TierInteract, tunnel.Classify("POST", "/api/board/p/tasks", ""))
	assert.Equal(t, tunnel.TierLaunch, tunnel.Classify("PUT", "/api/board/p/settings", ""), "the owner's alone since #5307; the handler refuses anyone else")
	assert.False(t, h.HubOperatorAllowed(authentic()), "still on")

	// The recent Operator actions are listed locally only.
	require.NoError(t, bs.RecordHubOperatorAction(context.Background(), board.HubOperatorAction{HubUser: "hub-user-1234abcd", Method: "POST", Path: "/api/board/p/tasks", Project: "p", Status: 200}))
	var got struct {
		Recent []board.HubOperatorAction `json:"recent_actions"`
	}
	require.NoError(t, json.Unmarshal(local("GET", "").Body.Bytes(), &got))
	require.Len(t, got.Recent, 1)
	assert.Equal(t, "hub-user-1234abcd", got.Recent[0].HubUser)
	assert.NotContains(t, tunneled("GET", "").Body.String(), "recent_actions")

	// v1.4.18's opt-in is dropped at startup.
	require.NoError(t, ss.SetSetting(context.Background(), legacyHubOperatorSetting, "true"))
	DropLegacyHubOperatorSetting(context.Background(), ss)
	settings, _ := ss.GetSettings(context.Background())
	_, still := settings[legacyHubOperatorSetting]
	assert.False(t, still)
	assert.Equal(t, "true", settings[HubOperatorDisabledSetting], "the opt-out is untouched")

	server, _ := setupSystemTestServer(t)
	for _, k := range []string{HubOperatorDisabledSetting, legacyHubOperatorSetting} {
		code, msg := putSettingsJSON(t, server.URL, map[string]any{k: "false"})
		assert.Equal(t, http.StatusBadRequest, code, k)
		assert.Contains(t, msg, "/api/system/hub-operator", k)
	}
}

func mustRecent(t *testing.T, handler *BoardHandler, ctx context.Context) []board.HubOperatorAction {
	t.Helper()
	recent, err := handler.bs.RecentHubOperatorActions(ctx, 1000)
	require.NoError(t, err)
	return recent
}

// #3942: a subscribe through Coral Hub subscribes the Coral Hub user the connector names, with
// the Hub user's role, not the "Operator" the dashboard asks for; never receive_mode all;
// never without the header. The Hub user's later posts aren't refused as
// a name already on the board. From this computer the body's names are used, as before.
func TestTunnelSubscribeIsTheHubUser(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	ctx := context.Background()
	g := TunnelIdentityGuard(handler.bs, func(*http.Request) bool { return true })
	router := chi.NewRouter()
	router.With(g).Post("/api/board/{project}/subscribe", handler.Subscribe)
	router.With(g).Post("/api/board/{project}/messages", handler.PostMessage)
	do := func(tunneled bool, target, body string, hubUser string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if tunneled {
			stamp(r)
		}
		if hubUser != "" {
			r.Header.Set(tunnel.HubUserHeader, hubUser)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}
	const user = "hub-user-1234abcd"
	// The guard still refuses orchestrator names, other members and receive_mode all first
	// (TestTunnelIdentityGuard); what passes it is the Operator, which Subscribe replaces.
	for _, body := range []string{
		`{"subscriber_id":"Operator","job_title":"Operator"}`, // what the dashboard sends
		`{"subscriber_id":"Operator","job_title":"Operator","session_name":"claude-x"}`,
	} {
		rec := do(true, "/api/board/p/subscribe", body, user)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", body, rec.Body.String())
	}
	subs, err := handler.bs.ListSubscribers(ctx, "p")
	require.NoError(t, err)
	require.Len(t, subs, 1, "only the Hub user, however the body named itself: %+v", subs)
	assert.Equal(t, user, subs[0].SubscriberID)
	assert.Equal(t, hubSubscriberTitle, subs[0].JobTitle)
	assert.Empty(t, subs[0].SessionName)
	assert.False(t, board.IsOrchestrator(subs[0].SubscriberID, subs[0].JobTitle, subs[0].CanPeek))

	rec := do(true, "/api/board/p/subscribe", `{"subscriber_id":"Operator","receive_mode":"all"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = do(true, "/api/board/p/subscribe", `{"subscriber_id":"Operator"}`, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "no Coral Hub user: refused (by the guard), never the Operator")
	assert.Equal(t, "hub_user_required", codeOf(t, rec))

	rec = do(true, "/api/board/p/messages", `{"content":"hello"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code, "the subscribed Hub user can still post: %s", rec.Body.String())

	rec = do(false, "/api/board/p/subscribe", `{"subscriber_id":"Operator","job_title":"Operator"}`, "")
	require.Equal(t, http.StatusOK, rec.Code)
	op, err := handler.bs.GetProjectSubscription(ctx, "p", "Operator")
	require.NoError(t, err)
	require.NotNil(t, op, "from this computer the dashboard still subscribes the Operator")
}
