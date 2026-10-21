package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5610: the route table (pkg/tunnel) is the one list of the owner's routes. The connector stamps the marker from it (pkg/tunnel's census, through the real connector) and the machine refuses a
// non-owner from it: this census sends, through the real router, a grantee and a marker of another label to EVERY route the table marks owner-only, and the owner too, and holds the answers.
func TestEveryOwnerOnlyRouteOfTheTableRefusesANonOwnerOnTheRealRouter(t *testing.T) {
	r := newE2ERemote(t, "owner-table", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	ctx := t.Context()
	r.srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	cs := store.NewCloudStore(r.db, store.NewRemoteServerStore(r.db, r.cfg.CoralDir()))
	require.NoError(t, cs.Save(ctx, "https://hub.example.invalid", "srv_1", "L", "coralhub_srv_abcd1234_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
	pinned, err := cs.PinOwnerUser(ctx, "srv_1", "hub-user-1234abcd")
	require.NoError(t, err)
	require.True(t, pinned)
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)

	owner := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-1234abcd", "Content-Type": "application/json"}
	grantee := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}
	otherOwner := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: "hub-user-9999ffff", "Content-Type": "application/json"}

	refused := func(code int, body string) bool {
		var out struct {
			Code string `json:"code"`
		}
		return code == 403 && json.Unmarshal([]byte(body), &out) == nil && out.Code == "owner_only"
	}
	checked := 0
	for _, route := range tunnel.OwnerRoutes() {
		if !route.Mode.Refuses() {
			continue
		}
		segs := strings.Split(strings.ReplaceAll(route.Pattern, "{hex64}", strings.Repeat("a", 64)), "/")
		for i, seg := range segs {
			if strings.HasPrefix(seg, "{") {
				segs[i] = "x"
			}
		}
		path := strings.Join(segs, "/")
		body := ""
		if route.Method != "GET" {
			body = "{}"
		}
		key := route.Method + " " + path
		g := call(h, route.Method, path, body, grantee)
		assert.True(t, refused(g.Code, g.Body.String()), "%s: a grantee gets 403 owner_only, got %d %s", key, g.Code, g.Body.String())
		o := call(h, route.Method, path, body, otherOwner)
		assert.True(t, refused(o.Code, o.Body.String()), "%s: a marker with another label gets 403 owner_only, got %d %s", key, o.Code, o.Body.String())
		ow := call(h, route.Method, path, body, owner)
		assert.False(t, refused(ow.Code, ow.Body.String()), "%s: the owner is not refused as a non-owner: %d %s", key, ow.Code, ow.Body.String())
		checked++
	}
	assert.GreaterOrEqual(t, checked, 14, "every owner-only route of the table was driven")
}

// The owner decision has one implementation: nothing but hub_owner.go reads the owner marker or runs the stamp checks, and every owner-only refusal goes through refuseNonOwner.
func TestTheOwnerDecisionHasOneImplementation(t *testing.T) {
	dir := filepath.Join("routes")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "hub_owner.go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		src := string(b)
		scanned++
		assert.NotContains(t, src, "identifyOwner(", "%s: the stamp checks live in hub_owner.go only", name)
		assert.NotContains(t, src, "tunnel.HubRoleHeader", "%s: the owner marker is read in hub_owner.go only", name)
		assert.NotContains(t, src, "refuseArtifactsToNonOwner", "%s: one refusal helper, refuseNonOwner", name)
	}
	assert.Greater(t, scanned, 100, "the scan must not go blind")
}

// The set of owner routes is pinned here, so that a route cannot leave the table (and stop being stamped and refused) without this test saying so: a change to it is a reviewed act.
func TestTheOwnerRoutesOfTheTableAreTheReviewedSet(t *testing.T) {
	want := map[string]string{
		"GET /api/artifacts": "owner-only", "GET /api/artifacts/{hex64}": "owner-only", "GET /api/artifacts/{hex64}/meta": "owner-only", "GET /api/artifacts/{hex64}/comments": "owner-only", "GET /api/artifacts/{hex64}/readers": "owner-only",
		"DELETE /api/artifacts/{hex64}": "owner-only", "GET /api/board/{project}/artifacts": "owner-only", "GET /api/board/{project}/tasks/{id}/artifact-content": "owner-only",
		"GET /api/session-artifacts": "owner-only", "GET /api/settings/prompt-inspection": "owner-only", "PUT /api/board/{project}/settings": "owner-only",
		"POST /api/board/{project}/cross-team/enable": "owner-only-hub", "POST /api/board/{project}/cross-team/disable": "owner-only-hub",
		"POST /api/board/{project}/cross-team/files/enable": "owner-only-hub", "POST /api/board/{project}/cross-team/files/disable": "owner-only-hub",
		"GET /api/board/{project}/cross-team/settings": "owner-only-hub",
		"GET /api/saved":                    "owner-only",
		"GET /api/saved/{id}/message":       "owner-only",
		"POST /api/saved":                   "owner-only",
		"PATCH /api/saved/{id}":             "owner-only",
		"DELETE /api/saved/{id}":            "owner-only",
		"GET /api/board/{project}/settings": "owner-sees", "GET /api/system/update": "owner-update", "POST /api/system/update/relaunch": "owner-update",
	}
	got := map[string]string{}
	for _, r := range tunnel.OwnerRoutes() {
		got[r.Method+" "+r.Pattern] = r.Mode.String()
	}
	assert.Equal(t, want, got)
}

const ownerGolden = "testdata/owner_routes.golden"

// Every route the server registers that the table marks as the owner's, with its tier and owner mode: the owner routes of the table and the routes of the server are the same set (a route
// the table marks that the server does not register, or the reverse, is a reviewed change). OWNER_GOLDEN_UPDATE=1 go test -tags 'dev fts5' ./internal/server -run OwnerRouteCensus
func TestOwnerRouteCensus(t *testing.T) {
	var lines []string
	for _, rt := range registeredRoutes(t) {
		method, route, _ := strings.Cut(rt, " ")
		if !strings.HasPrefix(route, "/api/") {
			continue
		}
		if _, mode := tunnel.OwnerRoute(method, concrete(route)); mode.Stamped() {
			lines = append(lines, method+" "+route+" "+tunnel.Classify(method, concrete(route), "").String()+" "+mode.String())
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	if os.Getenv("OWNER_GOLDEN_UPDATE") == "1" {
		require.NoError(t, os.WriteFile(ownerGolden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(ownerGolden)
	require.NoError(t, err)
	require.Equal(t, string(want), got, "the owner routes changed: review them, then OWNER_GOLDEN_UPDATE=1 go test -tags 'dev fts5' ./internal/server -run OwnerRouteCensus")
	assert.Len(t, lines, len(tunnel.OwnerRoutes()), "every owner route of the table is a route of the server")
}
