package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/store"
)

// #5577: per-user artifact ownership. An agent reads an artifact only when some record of it is owned by the owner of the agent's team (the team the launch path put it on); everything
// else answers exactly like an artifact that does not exist. The machine owner, the dashboard and the signed command line read everything.

func init() {
	// Every other test in this package: all sessions are on no team and everything is the machine owner's, the world before per-user ownership. A test that means otherwise sets its own
	// circle with useCircle and gets this one back.
	artifactCircle = operatorWorldCircle()
}

func operatorWorldCircle() ArtifactCircle {
	return ArtifactCircle{
		SessionBoard: func(context.Context, string) (string, error) { return "", nil },
		BoardOwner:   func(context.Context, string) (artifactPrincipal, error) { return operatorPrincipal, nil },
	}
}

// useCircle sets the artifact circle for one test and restores the previous one.
func useCircle(t *testing.T, c ArtifactCircle) {
	t.Helper()
	old := artifactCircle
	artifactCircle = c
	t.Cleanup(func() { artifactCircle = old })
}

// world is a small fixed world: sessions on boards, boards with owners.
type world struct {
	mu       sync.Mutex
	sessions map[string]string            // session -> board ("" none); a session that is absent is not live
	owners   map[string]artifactPrincipal // board -> owner; a board that is absent has no row (a board made before ids: the machine owner)
}

func (w *world) circle() ArtifactCircle {
	return ArtifactCircle{
		SessionBoard: func(_ context.Context, sid string) (string, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			b, ok := w.sessions[sid]
			if !ok {
				return "", errNoSuchSession
			}
			return b, nil
		},
		BoardOwner: func(_ context.Context, b string) (artifactPrincipal, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if o, ok := w.owners[b]; ok {
				return o, nil
			}
			return operatorPrincipal, nil
		},
	}
}

const (
	aliceUser = "hub-user-aaaaaaaa"
	bobUser   = "hub-user-bbbbbbbb"
)

func hubOwner(id string) artifactPrincipal { return artifactPrincipal{Kind: principalHubUser, ID: id} }

// A rig with two hub users' teams and the operator's: a1 and a2 are on team-a (alice's), b1 on team-b (bob's), o1 on team-o (the machine owner's), n1 on no team, and x1 is a live session that
// is on team-a only by a subscription it made itself (the rig's session table says it was launched onto team-o).
func ownerRig(t *testing.T) (*artifactRig, *world) {
	t.Helper()
	rig := uploadRig(t)
	w := &world{
		sessions: map[string]string{"a1": "team-a", "a2": "team-a", "b1": "team-b", "o1": "team-o", "n1": "", "x1": "team-o"},
		owners:   map[string]artifactPrincipal{"team-a": hubOwner(aliceUser), "team-b": hubOwner(bobUser), "team-o": operatorPrincipal},
	}
	useCircle(t, w.circle())
	oldLookup := uploaderLookup
	SetUploaderLookup(func(_ context.Context, sid string) (string, string, string) { // the records carry the board, so a link rule would have something to match
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.sessions[sid], sid, ""
	})
	t.Cleanup(func() { uploaderLookup = oldLookup })
	for _, s := range []string{"a1", "a2", "b1", "o1", "n1", "x1"} {
		require.NoError(t, rig.ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: s, AgentName: s, AgentType: "codex", WorkingDir: t.TempDir()}))
	}
	return rig, w
}

// get reads route as session (an agent's token), or as the dashboard when session is "".
func (rig *artifactRig) getAs(session, route string) (int, string) {
	req, err := http.NewRequest(http.MethodGet, rig.srv.URL+route, nil)
	require.NoError(rig.t, err)
	if session == "" {
		req.Header.Set(testDashboardHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(rig.t, err)
		defer resp.Body.Close()
		return resp.StatusCode, readAll(resp)
	}
	req.URL.RawQuery = strings.TrimPrefix(req.URL.RawQuery+"&session_id="+session, "&")
	resp, err := agentClient.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	return resp.StatusCode, readAll(resp)
}

func readAll(resp *http.Response) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

// upload as an agent of the world, and return the artifact id.
func (rig *artifactRig) uploadText(session, name, text string) string {
	code, out := rig.upload(session, name, "text/plain", strings.NewReader(text))
	require.Equal(rig.t, http.StatusCreated, code, out)
	return strings.TrimPrefix(out["uri"].(string), "coral://artifacts/")
}

func TestAnAgentReadsOnlyWhatItsTeamsOwnerOwns(t *testing.T) {
	rig, _ := ownerRig(t)
	aliceDoc := rig.uploadText("a1", "alice.txt", "alice's private note")
	bobDoc := rig.uploadText("b1", "bob.txt", "bob's private note")
	opDoc := rig.uploadText("o1", "operator.txt", "the machine owner's note")
	loose := rig.uploadText("n1", "loose.txt", "an agent on no team")

	type want struct {
		who, id string
		read    bool
	}
	for _, c := range []want{
		{"a1", aliceDoc, true}, {"a2", aliceDoc, true}, // the same team, the same owner
		{"b1", aliceDoc, false}, {"o1", aliceDoc, false}, {"n1", aliceDoc, false}, // another owner's team, the machine owner's team, no team
		{"a1", bobDoc, false}, {"b1", bobDoc, true}, {"o1", bobDoc, false},
		{"a1", opDoc, false}, {"b1", opDoc, false}, {"o1", opDoc, true}, {"n1", opDoc, true}, // a session on no team is the machine owner's
		{"n1", loose, true}, {"o1", loose, true}, {"a1", loose, false},
		{"", aliceDoc, true}, {"", bobDoc, true}, {"", opDoc, true}, // the dashboard reads everything
	} {
		for _, route := range []string{"/api/artifacts/%s?v=2", "/api/artifacts/%s/meta"} {
			r := strings.Replace(route, "%s", c.id, 1)
			status, body := rig.getAs(c.who, r)
			if c.read {
				assert.Equal(t, http.StatusOK, status, "%q reads %s: %s", c.who, r, body)
			} else {
				assert.Equal(t, http.StatusNotFound, status, "%q must not read %s", c.who, r)
			}
		}
	}
}

// An image the person pastes into an agent's chat is for that agent's team: its owner reads it, another owner's agents do not, and the dashboard always does.
func TestAPastedImageBelongsToTheTeamOfTheAgentItIsFor(t *testing.T) {
	rig, _ := ownerRig(t)
	code, out := postImage(t, "shot.png", cleanPNG(t), "a1")
	require.Equal(t, http.StatusOK, code, out)
	id := artifactOf(out)["id"].(string)
	for who, read := range map[string]bool{"a1": true, "a2": true, "b1": false, "o1": false, "": true} {
		status, _ := rig.getAs(who, "/api/artifacts/"+id+"/meta")
		assert.Equal(t, map[bool]int{true: http.StatusOK, false: http.StatusNotFound}[read], status, "%q", who)
	}
}

// Not yours looks exactly like it does not exist: the same status, headers and body, whatever the route.
func TestNotYoursIsTheSameAnswerAsMissing(t *testing.T) {
	rig, _ := ownerRig(t)
	secret := rig.uploadText("a1", "secret-name.txt", "alice's private note")
	missing := strings.Repeat("0", 64)
	for _, route := range []string{"/api/artifacts/%s?v=2", "/api/artifacts/%s/meta"} {
		notYours := rig.rawGet("b1", strings.Replace(route, "%s", secret, 1))
		absent := rig.rawGet("b1", strings.Replace(route, "%s", missing, 1))
		assert.Equal(t, absent.status, notYours.status, route)
		assert.Equal(t, absent.body, notYours.body, route)
		assert.Equal(t, absent.contentType, notYours.contentType, route)
		assert.NotContains(t, notYours.body, "secret-name", route)
	}
}

type rawAnswer struct {
	status      int
	body        string
	contentType string
}

func (rig *artifactRig) rawGet(session, route string) rawAnswer {
	status, body := rig.getAs(session, route)
	return rawAnswer{status: status, body: body, contentType: "json"}
}

// A damaged object does not tell a non-reader it exists: the gate runs before the corruption check.
func TestACorruptObjectAnswersAnUnauthorisedAgentAsMissing(t *testing.T) {
	rig, _ := ownerRig(t)
	id := rig.uploadText("a1", "alice.txt", "alice's private note")
	require.NoError(t, os.WriteFile(filepath.Join(rig.coralDir, "artifacts", "objects", id), []byte("short"), 0o600)) // the sidecar disagrees
	status, _ := rig.getAs("a1", "/api/artifacts/"+id)
	assert.Equal(t, http.StatusInternalServerError, status, "the owner's own agent is told the store is damaged")
	status, body := rig.getAs("b1", "/api/artifacts/"+id)
	assert.Equal(t, http.StatusNotFound, status, "another owner's agent is told nothing: %s", body)
	assert.NotContains(t, body, "damaged")
}

// Two owners of one digest: each reads (and keeps) it through its own record.
func TestTwoOwnersOfOneDigestEachRead(t *testing.T) {
	rig, _ := ownerRig(t)
	idA := rig.uploadText("a1", "n.txt", "the same bytes")
	idB := rig.uploadText("b1", "n.txt", "the same bytes")
	require.Equal(t, idA, idB)
	for _, who := range []string{"a1", "b1"} {
		status, _ := rig.getAs(who, "/api/artifacts/"+idA+"/meta")
		assert.Equal(t, http.StatusOK, status, who)
	}
	status, _ := rig.getAs("o1", "/api/artifacts/"+idA+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "the machine owner's team is a third owner")
}

// A record made before owners were kept is the machine owner's: its team's agents read it, another owner's do not.
func TestALegacyRecordIsTheMachineOwners(t *testing.T) {
	rig, _ := ownerRig(t)
	id := rig.uploadText("o1", "old.txt", "made before owners")
	rec := filepath.Join(rig.coralDir, "artifacts", "uploads", "o1", id+".json")
	data, err := os.ReadFile(rec)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	delete(m, "owner_kind")
	delete(m, "owner_id")
	out, _ := json.Marshal(m)
	require.NoError(t, os.WriteFile(rec, out, 0o600))
	for who, read := range map[string]bool{"o1": true, "n1": true, "a1": false, "b1": false, "": true} {
		status, _ := rig.getAs(who, "/api/artifacts/"+id+"/meta")
		assert.Equal(t, map[bool]int{true: http.StatusOK, false: http.StatusNotFound}[read], status, "%q", who)
	}
	// and an object with no record at all
	orphan, _, err := storeArtifact(rig.coralDir, "o1", "orphan.txt", "text/plain", []byte("an object with no upload record"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(rig.coralDir, "artifacts", "uploads", "o1", orphan+".json")))
	status, _ := rig.getAs("a1", "/api/artifacts/"+orphan+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "no record: the machine owner's")
	status, _ = rig.getAs("o1", "/api/artifacts/"+orphan+"/meta")
	assert.Equal(t, http.StatusOK, status)
}

// The check fails closed: an unset circle, a session the store does not know and a lookup that errors refuse an agent; the machine owner is not asked.
func TestTheReadCheckFailsClosed(t *testing.T) {
	rig, w := ownerRig(t)
	id := rig.uploadText("a1", "alice.txt", "alice's private note")
	delete(w.sessions, "a1") // not a live session any more
	status, _ := rig.getAs("a1", "/api/artifacts/"+id+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "an agent that cannot be placed")
	useCircle(t, ArtifactCircle{})
	status, _ = rig.getAs("o1", "/api/artifacts/"+id+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "an unset circle refuses every agent")
	status, _ = rig.getAs("", "/api/artifacts/"+id+"/meta")
	assert.Equal(t, http.StatusOK, status, "the dashboard is not asked")
	useCircle(t, ArtifactCircle{
		SessionBoard: func(context.Context, string) (string, error) { return "team-a", nil },
		BoardOwner:   func(context.Context, string) (artifactPrincipal, error) { return artifactPrincipal{}, os.ErrInvalid },
	})
	status, _ = rig.getAs("a1", "/api/artifacts/"+id+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "an owner lookup that errors")
}

// An agent's self-made subscription changes nothing: the check reads the team the launch path assigned (x1 was launched onto team-o but subscribed itself to team-a).
func TestASubscriptionAnAgentMadeItselfGrantsNoRead(t *testing.T) {
	rig, _ := ownerRig(t)
	id := rig.uploadText("a1", "alice.txt", "alice's private note")
	status, _ := rig.getAs("x1", "/api/artifacts/"+id+"/meta")
	assert.Equal(t, http.StatusNotFound, status, "x1 is on team-o by launch, whatever it subscribed to")
}

// The lists of an agent hold only what it may read, and the paging counts nothing else.
func TestAnAgentsListsHoldOnlyWhatItMayRead(t *testing.T) {
	_, h, coralDir := teamArtifactServer(t)
	w := &world{sessions: map[string]string{"a1": "team-a", "b1": "team-b"},
		owners: map[string]artifactPrincipal{"team-a": hubOwner(aliceUser), "team-b": hubOwner(bobUser)}}
	useCircle(t, w.circle())
	userA, _, err := storeArtifactBy(coralDir, "a1", "alice.md", "text/markdown", "", "", []byte("# alice\n"))
	require.NoError(t, err)
	userB, _, err := storeArtifactBy(coralDir, "b1", "bob.md", "text/markdown", "", "", []byte("# bob\n"))
	require.NoError(t, err)
	items := []teamArtifact{{ID: userA, managed: userA}, {ID: userB, managed: userB}, {ID: "task-5-0"}}
	asAgent := func(sid string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		return req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid}))
	}
	ids := func(in []teamArtifact) []string {
		var out []string
		for _, it := range in {
			out = append(out, it.ID)
		}
		return out
	}
	assert.Equal(t, []string{userA, "task-5-0"}, ids(filterReadable(asAgent("a1"), coralDir, items)), "alice's agent: its own, and a row that is no store object")
	assert.Equal(t, []string{userB, "task-5-0"}, ids(filterReadable(asAgent("b1"), coralDir, items)))
	assert.Len(t, filterReadable(httptest.NewRequest(http.MethodGet, "/x", nil), coralDir, items), 3, "not an agent: unchanged")
	useCircle(t, ArtifactCircle{})
	assert.Empty(t, filterReadable(asAgent("a1"), coralDir, items), "an unset circle empties an agent's list")
	_ = h
}

// What an agent may not read it may not use for a launch either: a launch is how an agent is given a team.
func TestAnAgentLaunchesOnlyOntoItsOwnersTeams(t *testing.T) {
	w := &world{sessions: map[string]string{"a1": "team-a"}, owners: map[string]artifactPrincipal{"team-a": hubOwner(aliceUser), "team-a2": hubOwner(aliceUser), "team-b": hubOwner(bobUser), "team-o": operatorPrincipal}}
	useCircle(t, w.circle())
	agent := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch", nil)
		return req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "a1"}))
	}
	assert.True(t, mayLaunchOnto(agent(), "team-a"), "its own team")
	assert.True(t, mayLaunchOnto(agent(), "team-a2"), "another team of the same owner")
	assert.False(t, mayLaunchOnto(agent(), "team-b"), "another owner's team")
	assert.False(t, mayLaunchOnto(agent(), "team-o"), "the machine owner's team")
	assert.True(t, mayLaunchOnto(httptest.NewRequest(http.MethodPost, "/api/sessions/launch", nil), "team-b"), "the Operator launches anywhere")
	owner, ok := launchOwner(agent())
	assert.True(t, ok)
	assert.Equal(t, hubOwner(aliceUser), owner, "a board an agent makes is its owner's")
	assert.Equal(t, aliceUser, owner.creator())
	assert.Equal(t, "Operator", operatorPrincipal.creator())
}

// The real lookups: the board store's created_by and the session store's launch board.
func TestTheServersCircleReadsTheStores(t *testing.T) {
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	b1, err := bs.CreateBoard(context.Background(), "alice team", aliceUser)
	require.NoError(t, err)
	b2, err := bs.CreateBoard(context.Background(), "ops team", "Operator")
	require.NoError(t, err)
	c := NewArtifactCircle(nil, bs)
	owner, err := c.BoardOwner(context.Background(), b1.ID)
	require.NoError(t, err)
	assert.Equal(t, hubOwner(aliceUser), owner)
	owner, err = c.BoardOwner(context.Background(), b2.ID)
	require.NoError(t, err)
	assert.Equal(t, operatorPrincipal, owner)
	owner, err = c.BoardOwner(context.Background(), "an-old-board-named-by-its-name")
	require.NoError(t, err)
	assert.Equal(t, operatorPrincipal, owner, "a board with no row is the machine owner's")
	_, err = c.SessionBoard(context.Background(), "s")
	assert.Error(t, err, "no session store: refused")
}

var _ = chi.NewRouter
var _ = board.ErrBoardNotFound

// The routes use the filter: an agent listing its own session's uploads does not see a record owned by someone else (here a record whose owner was changed after the upload), and the
// dashboard sees everything.
func TestTheSessionListOfAnAgentHidesWhatItMayNotRead(t *testing.T) {
	_, h := setupBoardTestServer(t)
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, nil)
	w := &world{sessions: map[string]string{"a1": "team-a"}, owners: map[string]artifactPrincipal{"team-a": hubOwner(aliceUser)}}
	useCircle(t, w.circle())
	mine, _, err := storeArtifactBy(coralDir, "a1", "mine.md", "text/markdown", "", "", []byte("# mine\n"))
	require.NoError(t, err)
	hers, _, err := storeArtifactBy(coralDir, "a1", "bobs.md", "text/markdown", "", "", []byte("# bobs\n"))
	require.NoError(t, err)
	rec := filepath.Join(coralDir, "artifacts", "uploads", "a1", hers+".json")
	data, err := os.ReadFile(rec)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	m["owner_kind"], m["owner_id"] = principalHubUser, bobUser
	out, _ := json.Marshal(m)
	require.NoError(t, os.WriteFile(rec, out, 0o600))

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if sid := req.Header.Get("X-Test-Session"); sid != "" {
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid}))
			}
			next.ServeHTTP(rw, req)
		})
	})
	r.Get("/api/session-artifacts", h.ListSessionArtifacts)
	r.Get("/api/board/{project}/artifacts", h.ListTeamArtifacts)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	subscribeSession(t, h, "alpha", "Carol", "a1")
	list := func(session string) []string {
		return listAt(t, srv.URL+"/api/session-artifacts?session_id=a1", session)
	}
	boardList := func(session string) []string { return listAt(t, srv.URL+"/api/board/alpha/artifacts", session) }
	assert.ElementsMatch(t, []string{mine}, boardList("a1"), "the team's list holds only what the agent may read")
	assert.ElementsMatch(t, []string{mine, hers}, boardList(""), "the dashboard sees both")
	assert.ElementsMatch(t, []string{mine}, list("a1"), "the agent sees its own, not the record owned by another user")
	assert.ElementsMatch(t, []string{mine, hers}, list(""), "the dashboard sees both")
}

func listAt(t *testing.T, url, session string) []string {
	t.Helper()
	{
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if session != "" {
			req.Header.Set("X-Test-Session", session)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Artifacts []struct{ ID string } `json:"artifacts"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		var ids []string
		for _, a := range body.Artifacts {
			ids = append(ids, a.ID)
		}
		return ids
	}
}

// The owner's view of who can read an artifact: the owners that hold it and their teams, from the same records the read check uses. Owner only.
func TestTheReadersViewShowsTheOwnersAndIsTheOwnersAlone(t *testing.T) {
	rig, _ := ownerRig(t)
	id := rig.uploadText("a1", "n.txt", "the same bytes")
	require.Equal(t, id, rig.uploadText("b1", "n.txt", "the same bytes"))

	status, body := rig.getAs("", "/api/artifacts/"+id+"/readers")
	require.Equal(t, http.StatusOK, status, body)
	var out struct {
		ID           string `json:"id"`
		MachineOwner string `json:"machine_owner"`
		Owners       []struct {
			Kind, ID string
			Records  int
			Teams    []struct{ ID string }
		} `json:"owners"`
		LinkedTeams []any  `json:"linked_teams"`
		Note        string `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Equal(t, id, out.ID)
	require.Len(t, out.Owners, 2, body)
	assert.Equal(t, aliceUser, out.Owners[0].ID)
	assert.Equal(t, "team-a", out.Owners[0].Teams[0].ID)
	assert.Equal(t, 1, out.Owners[0].Records)
	assert.Equal(t, 1, out.Owners[1].Records)
	assert.Equal(t, bobUser, out.Owners[1].ID)
	assert.NotNil(t, out.LinkedTeams)
	assert.Empty(t, out.LinkedTeams, "no cross-team read exists yet")
	assert.Contains(t, out.Note, "not an operating-system boundary")

	// an agent, even the owner's own agent, is refused; an artifact that does not exist is 404 for the owner
	for _, who := range []string{"a1", "b1", "o1"} {
		status, _ = rig.getAs(who, "/api/artifacts/"+id+"/readers")
		assert.Equal(t, http.StatusForbidden, status, who)
	}
	status, _ = rig.getAs("", "/api/artifacts/"+strings.Repeat("0", 64)+"/readers")
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = rig.getAs("", "/api/artifacts/not-an-id/readers")
	assert.Equal(t, http.StatusBadRequest, status)

	// through Coral Hub: the owner only
	for _, c := range hubCallers() {
		withLabel(t, c.label, nil)
		status, _ := hubGet(t, rig.srv.URL+"/api/artifacts/"+id+"/readers", c.header)
		if c.allow {
			assert.Equal(t, http.StatusOK, status, c.name)
		} else {
			assert.Equal(t, http.StatusForbidden, status, c.name)
		}
	}
}
