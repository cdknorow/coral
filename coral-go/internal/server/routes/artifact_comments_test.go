package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5176: comments on an artifact. Every role x action through the real routes, the stamped-is-never-owner rule, sizes and limits.

const (
	cmtDoc     = "d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0d0c0"
	cmtPic2    = "5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed"
	cmtPic     = "ba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5eba5e"
	cmtHubUser = "hub-user-3f2a9c01"
	cmtHubTwo  = "hub-user-77777777"
)

type cmtRig struct {
	t   *testing.T
	srv *httptest.Server
	h   *SessionsHandler
	ss  *store.SessionStore
}

func newCmtRig(t *testing.T) *cmtRig {
	t.Helper()
	cfg := config.Load(t.TempDir())
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	// The owner's identity here is a fixed install id (newCmtRigDefaultOwner leaves it to the real default, a random id kept in the settings).
	h.commentOwner = func(*http.Request) (string, string, string) { return "local", "install-owner-id", "" }
	r := chi.NewRouter()
	ident := testIdentity(h.db)
	r.Use(func(next http.Handler) http.Handler {
		withIdent := ident(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(testDashboardHeader) != "" {
				ctx := context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)
				if req.Header.Get(tunnel.ViaHeader) != "" {
					ctx = context.WithValue(req.Context(), callerKindKey{}, callerHub)
				}
				next.ServeHTTP(w, req.WithContext(ctx))
				return
			}
			withIdent.ServeHTTP(w, req)
		})
	})
	h.RegisterArtifactComments(r)
	h.RegisterAgentArtifacts(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	rig := &cmtRig{t: t, srv: srv, h: h, ss: ss}
	rig.artifact(cmtDoc, "plan.md", "text/markdown")
	rig.artifact(cmtPic, "shot.png", "image/png")
	rig.artifact(cmtPic2, "notes.txt", "text/plain")
	return rig
}

func (rig *cmtRig) artifact(id, name, mediaType string) {
	dir := rig.h.artifactDir()
	require.NoError(rig.t, os.MkdirAll(dir, 0o755))
	meta, _ := json.Marshal(agentArtifactMeta{Name: name, MediaType: mediaType, Size: 4, Digest: id})
	require.NoError(rig.t, os.WriteFile(filepath.Join(dir, id+".json"), meta, 0o644))
	require.NoError(rig.t, os.WriteFile(filepath.Join(dir, id), []byte("text"), 0o644))
}

var (
	cmtAsOwner  = map[string]string{testDashboardHeader: "1"}
	cmtAsHub    = map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: cmtHubUser}
	cmtAsHubTwo = map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: cmtHubTwo}
	cmtAsForged = map[string]string{tunnel.ViaHeader: "not-the-secret", tunnel.HubUserHeader: cmtHubUser}
	// The operator's own credential and the tunnel's stamp on one request: through the tunnel it is a hub viewer, whoever's credential it carries.
	cmtAsOwnerThroughTunnel = map[string]string{testDashboardHeader: "1", tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: cmtHubUser}
	cmtAsAgent              = map[string]string{testAgentHeader: "s1"}
	// Through Coral Hub the comments of an artifact are read by the machine's owner alone (#5575): the stamp, the owner marker and the enrolled label (withLabel(t, cmtHubUser)).
	cmtAsHubOwner = map[string]string{testDashboardHeader: "1", tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: cmtHubUser, tunnel.HubRoleHeader: "owner"}
)

func (rig *cmtRig) do(method, path string, header map[string]string, body any) (int, map[string]any) {
	rig.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, rig.srv.URL+path, rd)
	require.NoError(rig.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func cp(id string) string { return "/api/artifacts/" + id + "/comments" }

func anchorOf(q string) map[string]any {
	return map[string]any{"quote": q, "prefix": "before ", "suffix": " after", "start_line": 3, "end_line": 3}
}

func (rig *cmtRig) ownerComment(body string) int64 {
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": body, "anchor": anchorOf("the lock"), "board": "team-a"})
	require.Equal(rig.t, http.StatusCreated, code, fmt.Sprint(out))
	return int64(out["id"].(float64))
}

func (rig *cmtRig) ownerCommentOn(id, body string) int64 {
	code, out := rig.do("POST", cp(id), cmtAsOwner, map[string]any{"body": body, "anchor": anchorOf("x")})
	require.Equal(rig.t, http.StatusCreated, code, fmt.Sprint(out))
	return int64(out["id"].(float64))
}

func (rig *cmtRig) hubOn() {
	require.NoError(rig.t, rig.ss.SetSetting(context.Background(), ArtifactCommentsHubWriteSetting, "true"))
}

func TestArtifactCommentsOwnerLifecycle(t *testing.T) {
	rig := newCmtRig(t)
	id := rig.ownerComment("  Try 5,000 rows per batch.  ")
	code, out := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	list := out["comments"].([]any)
	require.Len(t, list, 1)
	c := list[0].(map[string]any)
	assert.Equal(t, "Try 5,000 rows per batch.", c["body"])
	assert.Equal(t, true, c["mine"])
	assert.Equal(t, map[string]any{"kind": "owner", "issuer": "local", "subject": "owner", "name": "You"}, c["author"], "the owner sees their own as You, never an id or a user name")
	assert.Equal(t, "the lock", c["anchor"].(map[string]any)["quote"])
	assert.Equal(t, map[string]any{"reply": true, "edit": true, "delete": true, "resolve": true}, c["can"])
	you := out["you"].(map[string]any)
	assert.Equal(t, true, you["can_comment"])
	assert.Equal(t, true, you["can_lock"])

	// A reply, one level.
	code, rep := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "Agreed.", "parent_id": id})
	require.Equal(t, 201, code, fmt.Sprint(rep))
	rid := int64(rep["id"].(float64))
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "to a reply", "parent_id": rid})
	assert.Equal(t, 400, code, "a reply to a reply")

	// Edit own, resolve and reopen, then delete.
	code, ed := rig.do("PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), id), cmtAsOwner, map[string]any{"body": "Edited."})
	require.Equal(t, 200, code)
	assert.Equal(t, "Edited.", ed["body"])
	assert.NotNil(t, ed["edited_at"])
	code, res := rig.do("POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), id), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.NotNil(t, res["resolved_at"])
	assert.Equal(t, map[string]any{"issuer": "local", "subject": "owner"}, res["resolved_by"])
	_, open := rig.do("GET", cp(cmtDoc)+"?state=open", cmtAsOwner, nil)
	assert.Empty(t, open["comments"], "a resolved thread leaves the open list")
	code, _ = rig.do("POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), rid), cmtAsOwner, nil)
	assert.Equal(t, 400, code, "a reply is not resolved on its own")
	code, re := rig.do("POST", fmt.Sprintf("%s/%d/reopen", cp(cmtDoc), id), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.Nil(t, re["resolved_at"])
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), id), cmtAsOwner, nil)
	assert.Equal(t, 200, code)
	_, after := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	list = after["comments"].([]any)
	require.Len(t, list, 2, "the deleted first comment stays as a tombstone for its reply")
	assert.Equal(t, true, list[0].(map[string]any)["deleted"])
	assert.Equal(t, "", list[0].(map[string]any)["body"])
	code, _ = rig.do("PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), id), cmtAsOwner, map[string]any{"body": "again"})
	assert.Equal(t, 404, code, "a deleted comment is gone")
}

func TestArtifactCommentsOwnerNeverDeletesOrEditsAnotherAuthors(t *testing.T) {
	rig := newCmtRig(t)
	rig.hubOn()
	code, out := rig.do("POST", cp(cmtDoc), cmtAsHub, map[string]any{"body": "from the hub", "anchor": anchorOf("x")})
	require.Equal(t, 201, code, fmt.Sprint(out))
	hubID := int64(out["id"].(float64))
	assert.Equal(t, map[string]any{"kind": "hub", "issuer": "coral-hub", "subject": cmtHubUser}, out["author"])
	code, out = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsOwner, nil)
	assert.Equal(t, 403, code, "the owner cannot delete a hub viewer's comment")
	assert.Equal(t, "comments_forbidden", out["code"])
	code, _ = rig.do("PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsOwner, map[string]any{"body": "rewritten"})
	assert.Equal(t, 403, code, "nor edit it")
	mine := rig.ownerComment("the owner's")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), mine), cmtAsHub, nil)
	assert.Equal(t, 403, code, "a hub viewer cannot delete the owner's")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsHubTwo, nil)
	assert.Equal(t, 403, code, "a hub viewer cannot delete another hub viewer's")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsHub, nil)
	assert.Equal(t, 200, code, "the author deletes their own")
	// Anyone who may write resolves any thread.
	code, _ = rig.do("POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), mine), cmtAsHubTwo, nil)
	assert.Equal(t, 200, code)
}

func TestArtifactCommentsAStampedRequestIsNeverTheOwner(t *testing.T) {
	rig := newCmtRig(t)
	id := rig.ownerComment("owner's")
	// The operator's credential carried through the tunnel is a hub viewer: it reads, it does not write (the hub flag is off), it cannot lock, and it is not the author.
	withLabel(t, cmtHubUser, nil)
	code, out := rig.do("GET", cp(cmtDoc), cmtAsOwnerThroughTunnel, nil)
	assert.Equal(t, 403, code, "through the hub the comments are the owner's to read: the operator's credential with the stamp but no owner marker is refused")
	code, out = rig.do("GET", cp(cmtDoc), cmtAsHubOwner, nil)
	require.Equal(t, 200, code)
	assert.Equal(t, "hub", out["you"].(map[string]any)["kind"])
	assert.Equal(t, false, out["you"].(map[string]any)["can_comment"])
	assert.Equal(t, false, out["you"].(map[string]any)["can_lock"])
	assert.Equal(t, false, out["comments"].([]any)[0].(map[string]any)["mine"], "the owner's comment is not mine to a stamped request")
	for _, c := range []struct{ method, path string }{
		{"POST", cp(cmtDoc)}, {"PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), id)}, {"DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), id)},
		{"POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), id)}, {"PUT", cp(cmtDoc) + "/lock"},
	} {
		code, _ := rig.do(c.method, c.path, cmtAsOwnerThroughTunnel, map[string]any{"body": "x", "anchor": anchorOf("y")})
		assert.Equal(t, 403, code, c.method+" "+c.path+" with the tunnel stamp and the operator's credential")
	}
	// With the hub flag on, it writes as the hub viewer, never as the owner, and still cannot delete the owner's comment or lock.
	rig.hubOn()
	code, out = rig.do("POST", cp(cmtDoc), cmtAsOwnerThroughTunnel, map[string]any{"body": "through the hub", "anchor": anchorOf("y")})
	require.Equal(t, 201, code, fmt.Sprint(out))
	assert.Equal(t, cmtHubUser, out["author"].(map[string]any)["subject"])
	assert.Equal(t, "hub", out["author"].(map[string]any)["kind"])
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), id), cmtAsOwnerThroughTunnel, nil)
	assert.Equal(t, 403, code)
	code, _ = rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwnerThroughTunnel, nil)
	assert.Equal(t, 403, code)
}

func TestArtifactCommentsForgedStampAndUnstampedHeaders(t *testing.T) {
	rig := newCmtRig(t)
	rig.hubOn()
	code, out := rig.do("GET", cp(cmtDoc), cmtAsForged, nil)
	assert.Equal(t, 403, code, "a stamp that is not the connector's is refused outright, reads included")
	assert.Equal(t, "forged_tunnel_stamp", out["code"])
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsForged, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 403, code)
	// The hub-user header without the stamp is ignored: the owner writes as the owner.
	code, out = rig.do("POST", cp(cmtDoc), map[string]string{testDashboardHeader: "1", tunnel.HubUserHeader: cmtHubUser}, map[string]any{"body": "as me", "anchor": anchorOf("y")})
	require.Equal(t, 201, code)
	assert.Equal(t, map[string]any{"kind": "owner", "issuer": "local", "subject": "owner", "name": "You"}, out["author"])
	stored, _ := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	assert.Equal(t, "install-owner-id", stored[len(stored)-1].AuthorSubject, "stored under the install's id, whatever the view says")
	// A body field that names an author is not read.
	code, out = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "claims", "anchor": anchorOf("y"), "author": map[string]any{"subject": "someone-else"}, "author_subject": "someone-else"})
	require.Equal(t, 201, code)
	assert.Equal(t, "owner", out["author"].(map[string]any)["subject"])
	// A hub viewer with no valid user header cannot write even with the flag on, and one with a malformed id has no identity either.
	for _, h := range []map[string]string{{tunnel.ViaHeader: cloud.ViaSecret()}, {tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: "hub-user-XYZ"}, {tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: cmtHubUser + cmtHubUser}} {
		code, _ = rig.do("POST", cp(cmtDoc), h, map[string]any{"body": "x", "anchor": anchorOf("y")})
		assert.Equal(t, 403, code, fmt.Sprint(h))
	}
	code, _ = rig.do("GET", cp(cmtDoc), map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, nil)
	assert.Equal(t, 403, code, "a hub viewer with no user id, and no owner marker, reads nothing (#5575)")
}

func TestArtifactCommentsHubViewersReadOnlyUntilTheFlagIsOn(t *testing.T) {
	rig := newCmtRig(t)
	rig.ownerComment("one")
	withLabel(t, cmtHubUser, nil)
	code, out := rig.do("GET", cp(cmtDoc), cmtAsHub, nil)
	assert.Equal(t, 403, code, "a hub viewer who is not the owner reads nothing (#5575)")
	code, out = rig.do("GET", cp(cmtDoc), cmtAsHubOwner, nil)
	require.Equal(t, 200, code)
	assert.Len(t, out["comments"], 1)
	assert.Equal(t, false, out["you"].(map[string]any)["can_comment"])
	assert.Contains(t, out["you"].(map[string]any)["reason"], "not turned on")
	code, out = rig.do("POST", cp(cmtDoc), cmtAsHub, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 403, code)
	assert.Contains(t, out["error"], "Coral Hub")
	rig.hubOn()
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsHub, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 201, code)
}

func TestArtifactCommentsAgentsReadTheirBoardsAndNeverWrite(t *testing.T) {
	rig := newCmtRig(t)
	rig.ownerComment("on team-a")
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "on team-b", "anchor": anchorOf("z"), "board": "team-b"})
	require.Equal(t, 201, code, fmt.Sprint(out))
	_, err := rig.h.bs.Subscribe(context.Background(), "team-a", "dev", "Developer", "agent-s1", nil, "")
	require.NoError(t, err)
	code, out = rig.do("GET", cp(cmtDoc), cmtAsAgent, nil)
	require.Equal(t, 200, code)
	list := out["comments"].([]any)
	require.Len(t, list, 1, "only the comments made on a board the agent is on")
	assert.Equal(t, "on team-a", list[0].(map[string]any)["body"])
	assert.Equal(t, "agent", out["you"].(map[string]any)["kind"])
	for _, c := range []struct{ method, path string }{{"POST", cp(cmtDoc)}, {"PUT", cp(cmtDoc) + "/lock"}} {
		code, _ = rig.do(c.method, c.path, cmtAsAgent, map[string]any{"body": "x", "anchor": anchorOf("y")})
		assert.Equal(t, 403, code, "an agent cannot "+c.method)
	}
	rig.hubOn()
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsAgent, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 403, code, "the hub flag is not an agent flag")
	code, out = rig.do("GET", cp(cmtDoc), map[string]string{"X-Coral-Session-Token": "no-such-token"}, nil)
	assert.Equal(t, 403, code, "an unknown token reads nothing")
	assert.Equal(t, "bad_agent_token", out["code"])
}

func TestArtifactCommentsRefuseCallersWithoutACredential(t *testing.T) {
	rig := newCmtRig(t)
	rig.ownerComment("one")
	code, out := rig.do("GET", cp(cmtDoc), nil, nil)
	assert.Equal(t, 403, code)
	assert.Equal(t, "sign_in_required", out["code"])
	code, _ = rig.do("POST", cp(cmtDoc), nil, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 403, code)
}

func TestArtifactCommentsLock(t *testing.T) {
	rig := newCmtRig(t)
	id := rig.ownerComment("one")
	code, out := rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.Equal(t, true, out["locked"])
	_, list := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Equal(t, true, list["locked"])
	assert.Equal(t, false, list["you"].(map[string]any)["can_comment"])
	code, out = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 403, code, "locked: even the owner's write is refused")
	assert.Contains(t, out["error"], "locked")
	for _, p := range []struct{ method, path string }{{"PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), id)}, {"POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), id)}} {
		code, _ = rig.do(p.method, p.path, cmtAsOwner, map[string]any{"body": "x"})
		assert.Equal(t, 403, code, "locked: "+p.method)
	}
	code, _ = rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Equal(t, 200, code, "reads stay open")
	code, _ = rig.do("DELETE", cp(cmtDoc)+"/lock", cmtAsOwner, nil)
	require.Equal(t, 200, code)
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "after", "anchor": anchorOf("y")})
	assert.Equal(t, 201, code)
	// A board lock covers a comment made on that board only.
	code, _ = rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, map[string]any{"scope": "board", "board": "team-a"})
	require.Equal(t, 200, code)
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "x", "anchor": anchorOf("y"), "board": "team-a"})
	assert.Equal(t, 403, code)
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "x", "anchor": anchorOf("y"), "board": "team-c"})
	assert.Equal(t, 201, code, "another board is not locked")
	code, _ = rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, map[string]any{"scope": "everything"})
	assert.Equal(t, 400, code)
	code, _ = rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, map[string]any{"scope": "board", "board": "bad board!"})
	assert.Equal(t, 400, code)
}

func TestArtifactCommentsSizesTypesAndIds(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	rig := newCmtRig(t)
	post := func(id string, body map[string]any) int {
		code, _ := rig.do("POST", cp(id), cmtAsOwner, body)
		return code
	}
	ok := map[string]any{"body": "fine", "anchor": anchorOf("q")}
	assert.Equal(t, 201, post(cmtDoc, ok))
	assert.Equal(t, 400, post(strings.ToUpper(cmtDoc), ok), "ids are lowercase hex")
	assert.Equal(t, 400, post("zz", ok))
	assert.Equal(t, 400, post(strings.Repeat("g", 64), ok))
	assert.Equal(t, 404, post(strings.Repeat("a", 64), ok), "no such artifact")
	assert.Equal(t, 415, post(cmtPic, ok), "an image takes no comments")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "   ", "anchor": anchorOf("q")}), "empty")
	assert.Equal(t, 413, post(cmtDoc, map[string]any{"body": strings.Repeat("x", store.MaxArtifactCommentBody+1), "anchor": anchorOf("q")}), "body over 8 KB")
	assert.Equal(t, 201, post(cmtDoc, map[string]any{"body": strings.Repeat("x", store.MaxArtifactCommentBody), "anchor": anchorOf("q")}), "exactly 8 KB")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"quote": strings.Repeat("q", maxCommentQuote+1)}}), "quote over 2 KB")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"quote": "q", "prefix": strings.Repeat("p", maxCommentContext+1)}}), "prefix over its limit")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x"}), "an anchor is required")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"quote": "  "}}), "an empty quote is not an anchor")
	assert.Equal(t, 201, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"whole": true}}), "a whole-document comment")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"quote": "q", "start_line": 9, "end_line": 3}}), "lines run backwards")
	assert.Equal(t, 400, post(cmtDoc, map[string]any{"body": "x", "anchor": map[string]any{"quote": "q"}, "board": "no spaces!"}), "a bad board name")
	assert.Equal(t, 413, post(cmtDoc, map[string]any{"body": strings.Repeat("x", 40<<10), "anchor": anchorOf("q")}), "a request far over the body cap")
	code, _ := rig.do("GET", cp(cmtDoc)+"?state=sideways", cmtAsOwner, nil)
	assert.Equal(t, 400, code)
	code, _ = rig.do("PATCH", cp(cmtDoc)+"/abc", cmtAsOwner, map[string]any{"body": "x"})
	assert.Equal(t, 400, code)
	code, _ = rig.do("PATCH", cp(cmtDoc)+"/99999", cmtAsOwner, map[string]any{"body": "x"})
	assert.Equal(t, 404, code)
	// The comment on the image artifact id is reachable for reading (an empty list) so an old comment stays readable if a type rule changes.
	code, out := rig.do("GET", cp(cmtPic), cmtAsOwner, nil)
	assert.Equal(t, 200, code)
	assert.Empty(t, out["comments"])
}

func TestArtifactCommentsCapPerArtifact(t *testing.T) {
	rig := newCmtRig(t)
	for i := 0; i < store.MaxArtifactComments; i++ {
		_, err := rig.ss.CreateArtifactComment(context.Background(), store.ArtifactComment{ArtifactID: cmtDoc, AnchorJSON: `{"whole":true}`, Body: "c", AuthorIssuer: "local", AuthorSubject: "install-owner-id", AuthorKind: "owner"})
		require.NoError(t, err)
	}
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "one more", "anchor": anchorOf("q")})
	assert.Equal(t, 409, code)
	assert.Equal(t, "too_many_comments", out["code"])
}

func TestArtifactCommentsRateLimitPerAuthor(t *testing.T) {
	rig := newCmtRig(t)
	rig.hubOn()
	for i := 0; i < maxCommentWrites; i++ {
		code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": fmt.Sprint("c", i), "anchor": anchorOf("q")})
		require.Equal(t, 201, code, fmt.Sprint(i, out))
	}
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "too many", "anchor": anchorOf("q")})
	assert.Equal(t, 429, code)
	assert.Equal(t, "rate_limited", out["code"])
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsHub, map[string]any{"body": "another author is not limited", "anchor": anchorOf("q")})
	assert.Equal(t, 201, code, "the limit is per author")
	code, _ = rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	assert.Equal(t, 200, code, "reads are not limited")
}

func TestArtifactCommentsAreRedactedWhenKept(t *testing.T) {
	rig := newCmtRig(t)
	secret := "ghp_" + strings.Repeat("a1B2c3D4", 5)
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "my token is " + secret + " ok", "anchor": anchorOf("q")})
	require.Equal(t, 201, code, fmt.Sprint(out))
	assert.NotContains(t, out["body"], secret, "a token pasted into a comment is not kept")
	list, err := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.NotContains(t, list[0].Body, secret)
}

// The Hub's keyword rules apply too (Orchestrator, round 2): a low-entropy api_key typed into a body or copied into an anchor is stored redacted.
func TestArtifactCommentsKeywordSecretsAreRedactedWhenKept(t *testing.T) {
	// Low-entropy values, at least as long as the Hub keyword rules' minimums (16 after an api key or token, 8 after a password, 12 in a query string).
	rig := newCmtRig(t)
	anchor := anchorOf("q")
	anchor["quote"] = "config: api_key=hunterhunterhunter1 here"
	anchor["prefix"] = "password=letmeinletmein "
	anchor["suffix"] = " token=abcabcabcabcabcabc"
	body := "use PGPASSWORD=pgsecretpgsecret psql, curl -H 'Cookie: a=1; session=cookiesecret1; b=2' tool --password clipassword1 --verbose " +
		"https://h/cb?x=1&api_key%3Durlsecreturlsecret%26y%3D2"
	secrets := []string{"hunterhunterhunter1", "letmeinletmein", "abcabcabcabcabcabc", "pgsecretpgsecret", "cookiesecret1", "clipassword1", "urlsecreturlsecret"}
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": body, "anchor": anchor})
	require.Equal(t, 201, code, fmt.Sprint(out))
	list, err := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	stored := fmt.Sprint(list[0])
	for _, leaked := range secrets {
		assert.NotContains(t, stored, leaked)
	}
	assert.Contains(t, list[0].Body, "--verbose", "the text around a redacted value is kept")
	// An edit goes through the same rule.
	id := fmt.Sprint(int64(out["id"].(float64)))
	code, out = rig.do("PATCH", cp(cmtDoc)+"/"+id, cmtAsOwner, map[string]any{"body": "now PGPASSWORD=secondsecondsecond and tool --password clitwoclitwo"})
	require.Equal(t, 200, code, fmt.Sprint(out))
	got, err := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.NoError(t, err)
	assert.NotContains(t, fmt.Sprint(got), "secondsecondsecond")
	assert.NotContains(t, fmt.Sprint(got), "clitwoclitwo")
}

func TestArtifactCommentNamesAreText(t *testing.T) {
	assert.Equal(t, "Casey", cleanCommentName("  Casey  "))
	assert.Equal(t, "ab", cleanCommentName("a\u202eb\u200b\n"), "bidi, zero-width and control characters are dropped")
	assert.Equal(t, 32, len([]rune(cleanCommentName(strings.Repeat("n", 100)))), "capped at 32 characters")
	assert.Equal(t, "", cleanCommentName("\u200b\u200c"))
}

func TestArtifactCommentsGoWithTheirArtifact(t *testing.T) {
	rig := newCmtRig(t)
	rig.ownerComment("one")
	other := rig.ownerCommentOn(cmtPic2, "stays")
	_ = other
	require.NoError(t, rig.ss.SetArtifactCommentsLocked(context.Background(), "artifact", cmtDoc, true))
	code, out := rig.do("DELETE", "/api/artifacts/"+cmtDoc, cmtAsOwner, nil)
	require.Equal(t, 200, code, fmt.Sprint(out))
	left, err := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.NoError(t, err)
	assert.Empty(t, left, "deleting the artifact removes its comments")
	locked, _ := rig.ss.ArtifactCommentsLocked(context.Background(), cmtDoc, "")
	assert.False(t, locked, "and its lock")
	kept, _ := rig.ss.ListArtifactComments(context.Background(), cmtPic2, "", nil)
	assert.Len(t, kept, 1, "another artifact's comments stay")
}

// The 429 for the write limit is the marked kind (X-Coral-Limit), so the dashboard's overload watch does not treat a person's busy hands as the server being overloaded.
func TestArtifactCommentsRateLimitIsAMarkedActionLimit(t *testing.T) {
	rig := newCmtRig(t)
	for i := 0; i < maxCommentWrites; i++ {
		code, _ := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": fmt.Sprint("c", i), "anchor": anchorOf("q")})
		require.Equal(t, 201, code)
	}
	req, _ := http.NewRequest("POST", rig.srv.URL+cp(cmtDoc), strings.NewReader(`{"body":"over","anchor":{"quote":"q"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(testDashboardHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 429, resp.StatusCode)
	assert.Equal(t, "action", resp.Header.Get("X-Coral-Limit"))
	assert.Equal(t, "60", resp.Header.Get("Retry-After"))
}

// A rig whose owner is the real default: issuer local and a random id kept in the settings (no override).
func newCmtRigDefaultOwner(t *testing.T) *cmtRig {
	rig := newCmtRig(t)
	rig.h.commentOwner = nil
	return rig
}

// Security's #5176 round 1: the operating-system user name must never reach a Coral Hub viewer or an agent, and the owner's subject is a random id of this install, not that name.
func TestArtifactCommentsNeverServeTheOSUserNameOrOwnerID(t *testing.T) {
	rig := newCmtRigDefaultOwner(t)
	code0, _ := rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	require.Equal(t, 200, code0)
	before, _ := rig.ss.GetSettings(context.Background())
	_, has := before[ArtifactCommentsOwnerIDSetting]
	assert.False(t, has, "a read makes no owner id: it writes no row")
	id := rig.ownerComment("the owner's own words")
	code, _ := rig.do("POST", fmt.Sprintf("%s/%d/resolve", cp(cmtDoc), id), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	st, err := rig.ss.GetSettings(context.Background())
	require.NoError(t, err)
	ownerID := st[ArtifactCommentsOwnerIDSetting]
	require.Len(t, ownerID, 32, "a random 128-bit id, kept")
	stored, _ := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.Len(t, stored, 1)
	assert.Equal(t, ownerID, stored[0].AuthorSubject)
	assert.Equal(t, "local", stored[0].AuthorIssuer)
	if u, err := osUserName(); err == nil && len(u) >= 3 {
		assert.NotEqual(t, u, stored[0].AuthorSubject, "the subject is not the user name")
	}
	// Every other viewer: a hub viewer through the stamp, an agent, and the owner themselves. None of the bodies contains the user name or the id.
	_, _ = rig.h.bs.Subscribe(context.Background(), "team-a", "dev", "Developer", "agent-s1", nil, "")
	for who, hdr := range map[string]map[string]string{"hub viewer": cmtAsHub, "hub viewer through the tunnel with the operator's credential": cmtAsOwnerThroughTunnel, "agent": cmtAsAgent, "owner": cmtAsOwner} {
		req, _ := http.NewRequest("GET", rig.srv.URL+cp(cmtDoc), nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(resp.Body)
		resp.Body.Close()
		body := raw.String()
		assert.NotContains(t, body, ownerID, who+": the owner's id never leaves the server")
		if u, err := osUserName(); err == nil && len(u) >= 3 && !strings.Contains("owner you", strings.ToLower(u)) {
			assert.NotContains(t, strings.ToLower(body), strings.ToLower(u), who+": the operating-system user name never leaves the server")
		}
		if who == "hub viewer" || who == "agent" {
			if resp.StatusCode == 200 && strings.Contains(body, "the owner's own words") {
				assert.Contains(t, body, `"name":"Owner"`, who+" sees the owner as Owner")
				assert.Contains(t, body, `"resolved_by":{"issuer":"local","subject":"owner"}`, who+": the resolver is the owner, by the fixed word")
			}
		}
		if who == "owner" {
			assert.Contains(t, body, `"name":"You"`)
		}
	}
	// The id is the same on every request (kept), so the owner keeps their edit rights.
	for i := 0; i < 3; i++ {
		assert.Equal(t, 200, func() int {
			c, _ := rig.do("PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), id), cmtAsOwner, map[string]any{"body": fmt.Sprint("edit ", i)})
			return c
		}())
	}
	st2, _ := rig.ss.GetSettings(context.Background())
	assert.Equal(t, ownerID, st2[ArtifactCommentsOwnerIDSetting])
}

func osUserName() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// Security's round 1, fix 2: the quote, prefix and suffix copy text of the artifact and are redacted at save like the body.
func TestArtifactCommentAnchorsAreRedactedWhenKept(t *testing.T) {
	rig := newCmtRig(t)
	secret := "ghp_" + strings.Repeat("a1B2c3D4", 5)
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "look", "anchor": map[string]any{"quote": "export TOKEN=" + secret + " done", "prefix": "before " + secret, "suffix": secret + " after"}})
	require.Equal(t, 201, code, fmt.Sprint(out))
	raw, _ := json.Marshal(out)
	assert.NotContains(t, string(raw), secret, "what the route answers")
	stored, err := rig.ss.ListArtifactComments(context.Background(), cmtDoc, "", nil)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.NotContains(t, stored[0].AnchorJSON, secret, "what the table keeps")
	assert.Contains(t, stored[0].AnchorJSON, "export TOKEN=", "the rest of the quote is kept")
}

// Security's round 1 (c): the caller comes first, so a caller who may never do the action cannot tell which artifact ids exist.
func TestArtifactCommentsAskTheCallerBeforeTheArtifact(t *testing.T) {
	rig := newCmtRig(t)
	missing := strings.Repeat("c", 64)
	for _, c := range []struct {
		name, method, path string
		hdr                map[string]string
	}{
		{"no credential, a missing artifact", "POST", cp(missing), nil},
		{"no credential, an image", "POST", cp(cmtPic), nil},
		{"an agent writing to a missing artifact", "POST", cp(missing), cmtAsAgent},
		{"an agent writing to an image", "POST", cp(cmtPic), cmtAsAgent},
		{"a hub viewer (writes off) to a missing artifact", "POST", cp(missing), cmtAsHub},
		{"a hub viewer to an image", "PATCH", cp(cmtPic) + "/1", cmtAsHub},
		{"an agent locking", "PUT", cp(missing) + "/lock", cmtAsAgent},
		{"no credential reading", "GET", cp(missing), nil},
	} {
		code, out := rig.do(c.method, c.path, c.hdr, map[string]any{"body": "x", "anchor": anchorOf("y")})
		assert.Equal(t, 403, code, c.name+": refused before the artifact is looked at, the same for a missing artifact and an image: "+fmt.Sprint(out))
	}
	// The owner, who may, does learn: 404 for a missing artifact, 415 for an image.
	code, _ := rig.do("POST", cp(missing), cmtAsOwner, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 404, code)
	code, _ = rig.do("POST", cp(cmtPic), cmtAsOwner, map[string]any{"body": "x", "anchor": anchorOf("y")})
	assert.Equal(t, 415, code)
}

// The owner's own delete stays open while the comments are locked (taking back what you wrote is not commenting); everything else is read only. A comment made with no board is not covered by a
// board lock ON PURPOSE in phase 1: only the owner writes, and the owner holds the lock. When hub writes turn on this must be revisited.
func TestArtifactCommentsLockedOwnerDeleteAndBoardlessComments(t *testing.T) {
	rig := newCmtRig(t)
	rig.hubOn()
	mine := rig.ownerComment("mine")
	code, out := rig.do("POST", cp(cmtDoc), cmtAsHub, map[string]any{"body": "hub's", "anchor": anchorOf("x"), "board": "team-a"})
	require.Equal(t, 201, code, fmt.Sprint(out))
	hubID := int64(out["id"].(float64))
	require.Equal(t, 200, func() int { c, _ := rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, nil); return c }())
	code, _ = rig.do("PATCH", fmt.Sprintf("%s/%d", cp(cmtDoc), mine), cmtAsOwner, map[string]any{"body": "edit while locked"})
	assert.Equal(t, 403, code, "no edit while locked")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsOwner, nil)
	assert.Equal(t, 403, code, "the owner still cannot delete another author's, locked or not")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), hubID), cmtAsHub, nil)
	assert.Equal(t, 403, code, "a hub viewer's own delete is refused while locked")
	code, _ = rig.do("DELETE", fmt.Sprintf("%s/%d", cp(cmtDoc), mine), cmtAsOwner, nil)
	assert.Equal(t, 200, code, "the owner takes back their own comment while locked")
	// Board lock and a comment with no board.
	require.Equal(t, 200, func() int { c, _ := rig.do("DELETE", cp(cmtDoc)+"/lock", cmtAsOwner, nil); return c }())
	require.Equal(t, 200, func() int {
		c, _ := rig.do("PUT", cp(cmtDoc)+"/lock", cmtAsOwner, map[string]any{"scope": "board", "board": "team-a"})
		return c
	}())
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "on the locked board", "anchor": anchorOf("x"), "board": "team-a"})
	assert.Equal(t, 403, code, "a comment on the locked board is refused")
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "no board at all", "anchor": anchorOf("x")})
	assert.Equal(t, 201, code, "a board-less comment is not covered by a board lock, on purpose (phase 1; revisit with hub writes)")
}
