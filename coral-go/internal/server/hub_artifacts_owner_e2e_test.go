package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5575 through the REAL connector path (QA, the 145 hold): a real hub session sends each request, the real connector (tunnel.LocalHandler with the production's AllowRequest and ViaValue, its
// stampRole and IsOwnerRoute) forwards it to the real router, and the machine's own owner check (hubOwnerRequest, with the enrolled owner label) decides. The earlier route tests set the owner
// headers by hand, so they could not see that the connector never stamped the artifact routes: the owner got 403 from the hub. Here the owner stream (role owner, the enrolled user) reads and
// deletes artifacts, and the grantee, the other hub user, an owner stream whose user is not the enrolled one, and a request carrying its own forged marker get nothing.
func TestHubArtifactsOwnerAndGranteeThroughTheRealConnector(t *testing.T) {
	const owner = "hub-user-1234abcd"
	const grantee = "hub-user-9999ffff"
	home := t.TempDir()
	setHome(t, home)
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), routes.MaxModeSetting, tunnel.TierLaunch.String()))
	cur := routes.MaxModeFromSettings(ss)
	routes.SetHubOwnerLabel(func(context.Context) (string, error) { return owner, nil }) // what the enrolment recorded
	t.Cleanup(func() { routes.SetHubOwnerLabel(nil) })

	// two artifacts in the real store: an agent's note and a pasted image's record
	objects := filepath.Join(cfg.CoralDir(), "artifacts", "objects")
	require.NoError(t, os.MkdirAll(objects, 0o700))
	put := func(name, mediaType, content string) string {
		sum := sha256.Sum256([]byte(content))
		id := hex.EncodeToString(sum[:])
		require.NoError(t, os.WriteFile(filepath.Join(objects, id), []byte(content), 0o600))
		meta, _ := json.Marshal(map[string]any{"name": name, "media_type": mediaType, "size": len(content), "digest": "sha256:" + id})
		require.NoError(t, os.WriteFile(filepath.Join(objects, id+".json"), meta, 0o600))
		return id
	}
	note := put("note.txt", "text/plain", "an agent's note")
	doomed := put("doomed.txt", "text/plain", "delete me through the hub")
	uploads := filepath.Join(cfg.CoralDir(), "artifacts", "uploads", "s-qa")
	require.NoError(t, os.MkdirAll(uploads, 0o700))
	rec, _ := json.Marshal(map[string]any{"name": "note.txt", "media_type": "text/plain", "size": 15, "digest": "sha256:" + note, "created_at": "2026-10-21T08:00:00Z", "uploader": "Dev", "role": "Developer", "board": "team-q"})
	require.NoError(t, os.WriteFile(filepath.Join(uploads, note+".json"), rec, 0o600))

	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{}); err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler:       &tunnel.LocalHandler{Redactor: secrets.Redactor(), BaseURL: local.URL, AllowRequest: tunnel.AllowRequest(cur), MaxMode: cur, ViaValue: cloud.ViaSecret()},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	sess := <-sessions
	t.Cleanup(func() { sess.Close() })

	// send is the hub: the stream carries the hub user and, for the account that owns this computer, the owner role (what coral-hub stamps on the stream); header is whatever a browser put on
	// the request itself.
	send := func(role, user, method, path string, header map[string]string) (int, string) {
		t.Helper()
		ctx := context.Background()
		if user != "" {
			ctx = tunnel.WithHubUser(ctx, user)
		}
		if role != "" {
			ctx = tunnel.WithHubRole(ctx, role)
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://ignored"+path, nil)
		require.NoError(t, err)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	reads := []string{
		"/api/artifacts/" + note + "?v=2", "/api/artifacts/" + note + "/meta", "/api/artifacts/" + note + "/comments",
		"/api/artifacts", "/api/board/team-q/artifacts", "/api/session-artifacts?session_id=s-qa",
	}

	for _, p := range reads {
		code, body := send("owner", owner, "GET", p, nil)
		assert.Equal(t, http.StatusOK, code, "the owner through the real connector: %s (%s)", p, body)
	}
	code, body := send("owner", owner, "GET", "/api/artifacts", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, note, "the machine-wide list shows the artifacts")
	assert.Contains(t, body, `"uploader":"Dev"`, "and who made them")

	for name, who := range map[string][2]string{
		"a grantee (no role)":                         {"", grantee},
		"the owner's own user without the owner role": {"", owner},
		"an owner stream with another hub user":       {"owner", grantee},
		"an owner stream with no hub user":            {"owner", ""},
		"a view role that is not owner":               {"viewer", owner},
		"an owner role written in capitals":           {"OWNER", owner},
	} {
		for _, p := range reads {
			code, body := send(who[0], who[1], "GET", p, nil)
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", name, p)
			assert.NotContains(t, body, "an agent's note", "%s: %s", name, p)
			assert.NotContains(t, body, note, "%s: %s leaks an id", name, p)
		}
	}

	// a request that carries its own marker and user is not the owner: the connector removes both
	forged := map[string]string{tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: owner, tunnel.ViaHeader: cloud.ViaSecret()}
	for _, p := range reads {
		code, _ := send("", grantee, "GET", p, forged)
		assert.Equal(t, http.StatusForbidden, code, "a forged marker on a grantee's request: %s", p)
		code, _ = send("", "", "GET", p, forged)
		assert.Equal(t, http.StatusForbidden, code, "a forged marker on a request with no hub user: %s", p)
	}

	// delete: the grantee and the others get 403 and nothing is deleted; the owner deletes and it is recorded
	for _, who := range [][2]string{{"", grantee}, {"owner", grantee}, {"", owner}} {
		code, _ := send(who[0], who[1], "DELETE", "/api/artifacts/"+doomed, nil)
		assert.Equal(t, http.StatusForbidden, code, "delete by %v", who)
	}
	_, err = os.Stat(filepath.Join(objects, doomed))
	require.NoError(t, err, "nothing was deleted by a refused caller")
	code, body = send("owner", owner, "DELETE", "/api/artifacts/"+doomed, nil)
	require.Equal(t, http.StatusOK, code, body)
	_, err = os.Stat(filepath.Join(objects, doomed))
	assert.True(t, os.IsNotExist(err), "the owner's delete removed it")
	acts, err := srv.boardStore.RecentHubOperatorActions(context.Background(), 50)
	require.NoError(t, err)
	found := false
	for _, a := range acts {
		if a.Method == "DELETE" && a.Path == "/api/artifacts/"+doomed && a.HubUser == owner && a.Status == http.StatusOK {
			found = true
		}
	}
	assert.True(t, found, "the delete is recorded in the Operator actions with the hub user: %v", acts)
}
