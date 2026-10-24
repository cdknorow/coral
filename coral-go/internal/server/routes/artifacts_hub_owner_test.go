package routes

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
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5575: through Coral Hub every artifact and pasted-image route is the machine's OWNER's alone, the owner can delete any artifact (recorded in the Operator actions), there is one
// machine-wide owner list, and each upload records who made it. A grantee, a request with a forged stamp, an owner whose enrolment has no label and an owner whose label differs are all
// refused, whatever the route's tier; this computer's dashboard is as before.

// hubCaller is one kind of request the artifact routes see, with the headers it carries and whether it should be let in.
type hubCaller struct {
	name   string
	header map[string]string
	label  string // the enrolled owner label the machine has recorded ("" = none)
	allow  bool
}

func hubCallers() []hubCaller {
	dash := map[string]string{testDashboardHeader: "1"}
	with := func(h map[string]string) map[string]string {
		out := map[string]string{testDashboardHeader: "1"}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	return []hubCaller{
		{"this computer's dashboard", dash, ownerLabel, true},
		{"the owner through the hub", with(ownerHeaders(ownerLabel)), ownerLabel, true},
		{"a grantee (no owner marker)", with(granteeHeaders()), ownerLabel, false},
		{"a forged via stamp", with(map[string]string{tunnel.ViaHeader: "x", tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: ownerLabel}), ownerLabel, false},
		{"the owner marker with no hub user", with(map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner"}), ownerLabel, false},
		{"the owner through a machine with no enrolled label", with(ownerHeaders(ownerLabel)), "", false},
		{"the marker of a hub user that is not the enrolled owner", with(ownerHeaders("hub-user-9999ffff")), ownerLabel, false},
		{"two owner markers", with(map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: ownerLabel, tunnel.HubRoleHeader: "owner, owner"}), ownerLabel, false},
	}
}

func hubGet(t *testing.T, url string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestArtifactReadsThroughTheHubAreOwnerOnly(t *testing.T) {
	rig := uploadRig(t)
	_, out := postImage(t, "p.png", cleanPNG(t), "s1")
	pasted := artifactOf(out)["id"].(string)
	code, up := rig.upload("s1", "note.txt", "text/plain", strings.NewReader("an agent's note"))
	require.Equal(t, http.StatusCreated, code, up)
	agents := strings.TrimPrefix(up["uri"].(string), "coral://artifacts/")

	for _, c := range hubCallers() {
		withLabel(t, c.label, nil)
		for _, id := range []string{pasted, agents} {
			for _, route := range []string{"/api/artifacts/" + id + "?v=2", "/api/artifacts/" + id + "/meta"} {
				status, body := hubGet(t, rig.srv.URL+route, c.header)
				if c.allow {
					assert.Equal(t, http.StatusOK, status, "%s: %s", c.name, route)
				} else {
					assert.Equal(t, http.StatusForbidden, status, "%s: %s", c.name, route)
					assert.NotContains(t, body, "p.png", c.name)
					assert.NotContains(t, body, "note.txt", c.name)
				}
			}
		}
		// the machine-wide list: the same callers
		status, body := hubGet(t, rig.srv.URL+"/api/artifacts", c.header)
		if c.allow {
			require.Equal(t, http.StatusOK, status, "%s: the list: %s", c.name, body)
			assert.Contains(t, body, pasted, c.name)
		} else {
			assert.Equal(t, http.StatusForbidden, status, "%s: the list", c.name)
			assert.NotContains(t, body, pasted, c.name)
		}
	}
}

func TestTeamArtifactListsThroughTheHubAreOwnerOnly(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	sid := "44444444-cccc-0000-0000-000000000004"
	subscribeSession(t, h, "alpha", "Carol", sid)
	pasted, _, err := storeArtifactBy(coralDir, sid, "Screenshot private.png", "image/png", uploadedByOwner, sourcePaste, cleanPNG(t))
	require.NoError(t, err)
	own, _, err := storeArtifact(coralDir, sid, "agent-made.md", "text/markdown", []byte("# an agent's own file\n"))
	require.NoError(t, err)
	for _, c := range hubCallers() {
		if strings.Contains(c.name, "dashboard") {
			continue // the team server here has no dashboard stand-in; an untunneled request is as before
		}
		withLabel(t, c.label, nil)
		for _, route := range []string{"/api/session-artifacts?session_id=" + sid, "/api/board/alpha/artifacts", "/api/board/alpha/tasks/1/artifact-content?index=0"} {
			status, body := hubGet(t, srv.URL+route, c.header)
			if c.allow {
				assert.NotEqual(t, http.StatusForbidden, status, "%s: %s", c.name, route)
				continue
			}
			assert.Equal(t, http.StatusForbidden, status, "%s: %s", c.name, route)
			assert.NotContains(t, body, pasted, c.name)
			assert.NotContains(t, body, own, c.name)
			assert.NotContains(t, body, "Screenshot private.png", c.name)
		}
	}
	// an untunneled request still lists both, pasted images included (nothing hides them any more)
	status, body := hubGet(t, srv.URL+"/api/session-artifacts?session_id="+sid, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, pasted)
	assert.Contains(t, body, own)
	// and the owner through the hub sees the pasted image too
	withLabel(t, ownerLabel, nil)
	status, body = hubGet(t, srv.URL+"/api/session-artifacts?session_id="+sid, ownerHeaders(ownerLabel))
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, pasted, "the owner sees every artifact, a pasted image included")
}

func TestArtifactCommentsReadThroughTheHubIsOwnerOnly(t *testing.T) {
	rig := uploadRig(t)
	code, up := rig.upload("s1", "note.txt", "text/plain", strings.NewReader("a note to comment on"))
	require.Equal(t, http.StatusCreated, code, up)
	id := strings.TrimPrefix(up["uri"].(string), "coral://artifacts/")
	// The comments read, behind the same stand-in dashboard caller as the rig (its router has no comment routes).
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
		})
	})
	r.Get("/api/artifacts/{id}/comments", rig.h.listArtifactComments)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	for _, c := range hubCallers() {
		withLabel(t, c.label, nil)
		status, _ := hubGet(t, srv.URL+"/api/artifacts/"+id+"/comments", c.header)
		if c.allow {
			assert.NotEqual(t, http.StatusForbidden, status, c.name)
		} else {
			assert.Equal(t, http.StatusForbidden, status, c.name)
		}
	}
}

func (rig *artifactRig) hubDelete(id string, header map[string]string) int {
	rig.t.Helper()
	code, _ := rig.del(id, header)
	return code
}

// The owner deletes any artifact through the hub; each delete is recorded in the Operator actions with the hub user, the id and the status; nobody else deletes anything.
func TestTheHubOwnerDeletesAnyArtifactAndItIsRecorded(t *testing.T) {
	rig := uploadRig(t)
	_, out := postImage(t, "p.png", cleanPNG(t), "s1")
	pasted := artifactOf(out)["id"].(string)
	code, up := rig.upload("s2", "note.txt", "text/plain", strings.NewReader("an agent's note"))
	require.Equal(t, http.StatusCreated, code, up)
	agents := strings.TrimPrefix(up["uri"].(string), "coral://artifacts/")
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(rig.coralDir, "artifacts", "objects", id))
		return err == nil
	}
	actions := func() []string {
		got, err := rig.h.bs.RecentHubOperatorActions(context.Background(), 50)
		require.NoError(t, err)
		var rows []string
		for _, a := range got {
			rows = append(rows, a.HubUser+" "+a.Method+" "+a.Path+" "+strconv.Itoa(a.Status))
		}
		return rows
	}

	for _, c := range hubCallers() {
		if c.allow {
			continue
		}
		withLabel(t, c.label, nil)
		assert.Equal(t, http.StatusForbidden, rig.hubDelete(pasted, c.header), c.name)
		assert.Equal(t, http.StatusForbidden, rig.hubDelete(agents, c.header), c.name)
	}
	require.True(t, exists(pasted) && exists(agents), "no refused caller deleted anything")
	assert.Empty(t, actions(), "a refusal is not an Operator action")

	withLabel(t, ownerLabel, nil)
	owner := map[string]string{testDashboardHeader: "1", tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: ownerLabel}
	assert.Equal(t, http.StatusOK, rig.hubDelete(pasted, owner), "a pasted image goes")
	assert.Equal(t, http.StatusOK, rig.hubDelete(agents, owner), "so does an agent's artifact")
	assert.False(t, exists(pasted) || exists(agents), "both are gone from the store")
	assert.Equal(t, http.StatusNotFound, rig.hubDelete(agents, owner), "deleting it again finds nothing")
	rows := actions()
	assert.Contains(t, rows, ownerLabel+" DELETE /api/artifacts/"+pasted+" 200")
	assert.Contains(t, rows, ownerLabel+" DELETE /api/artifacts/"+agents+" 200")
	assert.Contains(t, rows, ownerLabel+" DELETE /api/artifacts/"+agents+" 404", "the status of a delete that found nothing is recorded too")
	assert.Len(t, rows, 3)

	// an agent's token and this computer's dashboard are as before
	_, o2 := postImage(t, "q.png", cleanPNG(t), "s1")
	local := artifactOf(o2)["id"].(string)
	assert.Equal(t, http.StatusOK, rig.hubDelete(local, map[string]string{testDashboardHeader: "1"}))
	assert.Len(t, actions(), 3, "a local delete is not a hub action")
}

// Each upload records the board, the agent's name and its role as they were then; the dashboard's paste records the human too; older records have none.
func TestUploadsRecordWhoMadeThem(t *testing.T) {
	rig := uploadRig(t)
	old := uploaderLookup
	t.Cleanup(func() { uploaderLookup = old })
	uploaderLookup = NewUploaderLookup(rig.ss, rig.h.bs)
	_, err := rig.h.bs.Subscribe(context.Background(), "team-x", "Backend Dev", "Backend Dev", "codex-s1", nil, "all")
	require.NoError(t, err)

	code, up := rig.upload("s1", "note.txt", "text/plain", strings.NewReader("who made this"))
	require.Equal(t, http.StatusCreated, code, up)
	agents := strings.TrimPrefix(up["uri"].(string), "coral://artifacts/")
	rec, ok := readUploadRecord(rig.coralDir, "s1", agents)
	require.True(t, ok)
	assert.Equal(t, "a1", rec.Uploader, "the agent's name")
	assert.Equal(t, "Backend Dev", rec.Role, "its role on the board")
	assert.Equal(t, "team-x", rec.Board, "and the board")
	assert.Empty(t, rec.UploadedBy, "an agent's own upload names no human")

	_, out := postImage(t, "p.png", cleanPNG(t), "s2")
	pasted, ok := readUploadRecord(rig.coralDir, "s2", artifactOf(out)["id"].(string))
	require.True(t, ok)
	assert.Equal(t, uploadedByOwner, pasted.UploadedBy, "the paste names the human")
	assert.Equal(t, "a2", pasted.Uploader, "and the agent it was pasted for")
	assert.Empty(t, pasted.Board, "s2 is on no board")

	// a lookup that fails, hangs on nothing, or returns text with control characters records none of it
	uploaderLookup = func(context.Context, string) (string, string, string) {
		return "b\x00", "n\u202e", strings.Repeat("r", 500)
	}
	_, _, err = storeArtifactBy(rig.coralDir, "s1", "x.txt", "text/plain", "", "", []byte("again"))
	require.NoError(t, err)
	sum := sha256Hex([]byte("again"))
	bad, ok := readUploadRecord(rig.coralDir, "s1", sum)
	require.True(t, ok)
	assert.Empty(t, bad.Board+bad.Uploader+bad.Role, "unclean labels are dropped, never stored")

	// the list says "unknown" for a record from before these fields
	uploaderLookup = nil
	_, _, err = storeArtifact(rig.coralDir, "s2", "older.txt", "text/plain", []byte("an older record"))
	require.NoError(t, err)
	status, body := hubGet(t, rig.srv.URL+"/api/artifacts", map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, status, body)
	var list struct {
		Artifacts []allArtifact `json:"artifacts"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	rows := map[string]allArtifact{}
	for _, a := range list.Artifacts {
		rows[a.ID] = a
	}
	oldRow := rows[sha256Hex([]byte("an older record"))]
	assert.Equal(t, "unknown", oldRow.Uploader)
	assert.Equal(t, "unknown", oldRow.BoardLabel)
	assert.Equal(t, "upload", oldRow.Source)
	assert.Equal(t, "s2", oldRow.SessionID)
	row := rows[agents]
	assert.Equal(t, "a1", row.Uploader)
	assert.Equal(t, "Backend Dev", row.Role)
	assert.Equal(t, "team-x", row.Board)
	assert.Equal(t, "upload", row.Source)
	assert.True(t, row.Available)
	assert.Equal(t, int64(len("who made this")), row.Size)
	pasteRow := rows[artifactOf(out)["id"].(string)]
	assert.Equal(t, "paste", pasteRow.Source)
	assert.Equal(t, "owner", pasteRow.Human)
}

// The machine-wide list pages by a cursor, newest first, with every artifact once and a bad cursor refused.
func TestTheMachineWideListPagesByCursor(t *testing.T) {
	rig := uploadRig(t)
	const n = 7
	var ids []string
	for i := 0; i < n; i++ {
		_, meta, err := storeArtifact(rig.coralDir, "s1", "f"+string(rune('a'+i))+".txt", "text/plain", []byte("file number "+string(rune('a'+i))))
		require.NoError(t, err)
		ids = append(ids, strings.TrimPrefix(meta.Digest, "sha256:"))
	}
	dash := map[string]string{testDashboardHeader: "1"}
	seen := map[string]int{}
	url := rig.srv.URL + "/api/artifacts?limit=3"
	pages := 0
	for url != "" {
		status, body := hubGet(t, url, dash)
		require.Equal(t, http.StatusOK, status, body)
		var page struct {
			Artifacts  []allArtifact `json:"artifacts"`
			Total      int           `json:"total"`
			NextCursor string        `json:"next_cursor"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &page))
		require.Equal(t, n, page.Total)
		assert.LessOrEqual(t, len(page.Artifacts), 3)
		for _, a := range page.Artifacts {
			seen[a.ID]++
		}
		pages++
		require.Less(t, pages, 10, "the cursor must end")
		url = ""
		if page.NextCursor != "" {
			url = rig.srv.URL + "/api/artifacts?limit=3&cursor=" + page.NextCursor
		}
	}
	assert.Equal(t, 3, pages)
	for _, id := range ids {
		assert.Equal(t, 1, seen[id], "each artifact exactly once: %s", id)
	}
	for _, bad := range []string{"cursor=!!!", "cursor=" + strings.Repeat("A", 300), "limit=0", "limit=201", "limit=x", "board=../x", "extra=1"} {
		status, _ := hubGet(t, rig.srv.URL+"/api/artifacts?"+bad, dash)
		assert.Equal(t, http.StatusBadRequest, status, bad)
	}
	// an agent's token may not list every team's files
	status, _ := hubGet(t, rig.srv.URL+"/api/artifacts", map[string]string{testAgentHeader: "s1"})
	assert.Equal(t, http.StatusForbidden, status, "an agent")
	status, _ = hubGet(t, rig.srv.URL+"/api/artifacts", nil)
	assert.NotEqual(t, http.StatusOK, status, "no credential")
}

func sha256Hex(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// setHubOwner makes req a request through Coral Hub from the machine's owner (the connector's stamp, the owner marker, the hub user) and has the machine's enrolment name that owner.
func setHubOwner(t *testing.T, req *http.Request) {
	t.Helper()
	withLabel(t, ownerLabel, nil)
	for k, v := range ownerHeaders(ownerLabel) {
		req.Header.Set(k, v)
	}
}

// #7061: an organisation's admin deleting an artifact through the Hub is recorded in the Operator actions with the label AND the stable id, so a rename or a removed member never re-attributes it;
// a member is refused and nothing is recorded; a personal machine's row has no id.
func TestATeamAdminsDeleteIsRecordedWithTheirStableID(t *testing.T) {
	rig := uploadRig(t)
	_, out := postImage(t, "p.png", cleanPNG(t), "s1")
	pasted := artifactOf(out)["id"].(string)
	withLabel(t, "", nil)
	withRecord(t, "team", teamOrgA, nil)
	asTeam := func(role string) map[string]string {
		h := cmtAsTeam(teamOrgA, role, teamUIDA, teamUser)
		h[testDashboardHeader] = "1"
		return h
	}
	assert.Equal(t, http.StatusForbidden, rig.hubDelete(pasted, asTeam("member")))
	got, err := rig.h.bs.RecentHubOperatorActions(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, got, "a refusal is not an Operator action")

	assert.Equal(t, http.StatusOK, rig.hubDelete(pasted, asTeam("admin")))
	got, err = rig.h.bs.RecentHubOperatorActions(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, teamUser, got[0].HubUser)
	assert.Equal(t, teamUIDA, got[0].HubUserID)
	assert.NotContains(t, got[0].Path, teamOrgA, "the organisation's id is not in the record")
}
