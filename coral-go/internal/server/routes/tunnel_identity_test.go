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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/tunnel"
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

// #3377 (SEC-BD-02): through the tunnel nobody acts with an orchestrator's rights, and
// the Operator only with the owner's opt-in. Requests from this computer are unchanged.
func TestTunnelIdentityGuard(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := handler.bs.Subscribe(ctx, "p", "Lead", "Developer", "s1", nil, nil, "", true) // can_peek
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, "p", "Dev", "Developer", "s2", nil, nil, "")
	require.NoError(t, err)

	operatorOK := false
	passed := 0
	g := TunnelIdentityGuard(handler.bs, func(*http.Request) bool { return operatorOK })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed++ }))
	do := func(tunneled bool, method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if tunneled {
			stamp(r)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, r)
		return rec
	}

	// Orchestrator self-promotion and impersonation.
	for _, tc := range []struct{ target, body string }{
		{"/api/board/p/subscribe", `{"subscriber_id":"w","job_title":"Orchestrator"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"my-orchestrator"}`},
		{"/api/board/p/subscribe", `{"subscriber_id":"w","receive_mode":"all"}`},
		{"/api/board/p/tasks/claim", `{"subscriber_id":"Lead"}`}, // an existing peek-capable subscriber
		{"/api/board/p/tasks", `{"created_by":"Orchestrator","title":"x"}`},
	} {
		rec := do(true, "POST", tc.target, tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.body)
		assert.Equal(t, "orchestrator_identity", codeOf(t, rec), tc.body)
	}

	// The Operator: refused by default in any identity field, allowed (and logged) with the opt-in.
	for _, tc := range []struct{ method, target, body string }{
		{"POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`},
		{"PATCH", "/api/board/p/tasks/1", `{"subscriber_id":"Dev","assigned_to":"operator"}`},
		{"POST", "/api/board/p/tasks/1/reassign", `{"subscriber_id":"Dev","assignee":"Operator"}`},
		{"DELETE", "/api/board/p/waits?subscriber_id=Operator", ``},
	} {
		rec := do(true, tc.method, tc.target, tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.body)
		assert.Equal(t, "operator_identity_required", codeOf(t, rec), tc.body)
	}
	before := passed
	operatorOK = true
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`).Code)
	assert.Equal(t, before+1, passed)
	operatorOK = false

	// Unchanged: an ordinary member through the tunnel, reads, chat posts (their sender is
	// the Hub user), and everything from this computer.
	before = passed
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/tasks/claim", `{"subscriber_id":"Dev"}`).Code)
	assert.Equal(t, http.StatusOK, do(true, "GET", "/api/board/p/messages?subscriber_id=Operator", ``).Code)
	assert.Equal(t, http.StatusOK, do(true, "POST", "/api/board/p/messages", `{"subscriber_id":"Operator","content":"hi"}`).Code)
	assert.Equal(t, http.StatusOK, do(false, "POST", "/api/board/p/tasks", `{"subscriber_id":"Operator","title":"x"}`).Code)
	assert.Equal(t, http.StatusOK, do(false, "POST", "/api/board/p/subscribe", `{"subscriber_id":"Orchestrator","receive_mode":"all"}`).Code)
	assert.Equal(t, before+5, passed)
}

// A tunnel chat post is sent as the Coral Hub user from the header, marked origin
// hub_user; never the Operator, never a member's name. The header means nothing locally.
func TestTunnelChatPostIsTheHubUser(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	_, err := handler.bs.Subscribe(context.Background(), "p", "hub-user-0000beef", "Developer", "s3", nil, nil, "")
	require.NoError(t, err) // a member created before the name was reserved
	post := func(tunneled bool, hubUser, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", server.URL+"/api/board/p/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if tunneled {
			stamp(req)
		}
		if hubUser != "" {
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
	for _, bad := range []string{"", "hub-user-XYZ", "Operator", "@Lead", "hub-user-1234567890", "hub:1234abcd"} {
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

	// From this computer the header is ignored: the body's sender, no origin.
	code, out = post(false, "hub-user-1234abcd", `{"subscriber_id":"Operator","content":"local"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Operator", out["subscriber_id"])
	assert.Nil(t, out["origin"])
	// A local client can't set origin either (it isn't read from the body).
	code, out = post(false, "", `{"subscriber_id":"Dev","content":"x","origin":"hub_user"}`)
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

// #3377 (SEC-JB-01): through the tunnel an auto-accepting run (the flag, or the CLI flags
// themselves) needs the local allow-hub-auto-accept switch and Launch mode; repo_path
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
	auto := `{"prompt":"p","repo_path":"` + repo + `","auto_accept":true}`
	flags := `{"prompt":"p","repo_path":"` + repo + `","flags":"--dangerously-skip-permissions"}`

	code, c := run(true, true, auto)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "hub_mode_too_low", c, "max mode defaults to view")
	require.NoError(t, ss.SetSetting(context.Background(), MaxModeSetting, "launch"))
	code, _ = run(true, true, auto)
	assert.Equal(t, http.StatusOK, code, "Launch mode, switch on by default")
	code, c = run(true, false, auto)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "no_dashboard_provenance", c, "a forged stamp")
	require.NoError(t, ss.SetSetting(context.Background(), AllowHubAutoAcceptSetting, "false"))
	for _, b := range []string{auto, flags} {
		code, c = run(true, true, b)
		assert.Equal(t, http.StatusForbidden, code, b)
		assert.Equal(t, "hub_auto_accept_disabled", c, b)
	}
	code, _ = run(false, false, auto)
	assert.Equal(t, http.StatusOK, code, "from this computer: unchanged")
	code, _ = run(true, true, `{"prompt":"p","repo_path":"`+repo+`"}`)
	assert.Equal(t, http.StatusOK, code, "no auto-accept: no switch needed")

	notGit := t.TempDir()
	code, _ = run(false, false, `{"prompt":"p","repo_path":"`+notGit+`"}`)
	assert.Equal(t, http.StatusBadRequest, code, "repo_path must be a git work tree")
	_ = os.RemoveAll(notGit)
}

// The 'Let Coral Hub act as the Operator' switch: local dashboard only (404 through the
// tunnel), off by default, not settable with PUT /api/settings, honoured only on an
// authentic tunnel request while linked.
func TestHubOperatorSetting(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	h := NewCloudHandler(ss, t.TempDir())
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
	rec := local("GET", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"enabled":false,"linked":false}`, rec.Body.String())
	rec = local("PUT", `{"enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"enabled":true,"linked":false}`, rec.Body.String())
	assert.Equal(t, http.StatusBadRequest, local("PUT", `{}`).Code)

	tr := stamp(httptest.NewRequest("PUT", "/api/system/hub-operator", strings.NewReader(`{"enabled":true}`)))
	trec := httptest.NewRecorder()
	h.HubOperator(trec, tr)
	assert.Equal(t, http.StatusNotFound, trec.Code, "never through the tunnel")

	// On, but this server isn't linked to Coral Hub: not honoured.
	assert.False(t, h.HubOperatorAllowed(stamp(httptest.NewRequest("POST", "/api/board/p/tasks", nil))))
	assert.False(t, h.HubOperatorAllowed(httptest.NewRequest("POST", "/api/board/p/tasks", nil)))

	server, _ := setupSystemTestServer(t)
	code, msg := putSettingsJSON(t, server.URL, map[string]any{HubOperatorSetting: "true"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, msg, "/api/system/hub-operator")
}
