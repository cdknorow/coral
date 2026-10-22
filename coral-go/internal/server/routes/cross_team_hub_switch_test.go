package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5572: Coral Hub's owner turns a team's cross-team messaging and file sharing on or off through four dedicated routes, behind the local lock, one transaction with the Operator-actions row,
// announced on the team's board, 10 a minute per team.

type hubSwitchEnv struct {
	t      *testing.T
	h      *BoardHandler
	bs     *board.Store
	dbPath string
	clock  time.Time
	mu     sync.Mutex
	lock   bool
	lockEr error
}

func newHubSwitchEnv(t *testing.T) *hubSwitchEnv {
	t.Helper()
	e := &hubSwitchEnv{t: t, dbPath: t.TempDir() + "/board.db", lock: true, clock: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	bs, err := boardtest.Open(e.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e.bs = bs
	e.h = NewBoardHandler(bs)
	t.Cleanup(e.h.WaitBackground)
	e.h.crossTeam.now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.clock }
	e.h.SetHubCrossTeamLock(func(context.Context) (bool, error) { return e.lock, e.lockEr })
	withLabel(t, ownerLabel, nil)
	for _, team := range []string{"team", "other"} {
		_, err := bs.Subscribe(context.Background(), team, "Orchestrator", "Orchestrator", team+"-orch", nil, "mentions")
		require.NoError(t, err)
		// The switches default to on (#5916): these tests are about the owner turning them on and off, so each team starts with both explicitly off.
		off := false
		_, err = bs.UpdateBoardSettings(context.Background(), team, board.BoardSettingsUpdate{CrossTeamMessaging: &off, CrossTeamArtifacts: &off})
		require.NoError(t, err)
	}
	return e
}

func (e *hubSwitchEnv) advance(d time.Duration) { e.mu.Lock(); e.clock = e.clock.Add(d); e.mu.Unlock() }

// call runs one of the four routes ("enable", "disable", "files/enable", "files/disable") on team.
func (e *hubSwitchEnv) call(team, route, body string, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	path := "/api/board/" + team + "/cross-team/" + route
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", team)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	switch route {
	case "enable":
		e.h.HubCrossTeamEnable(rec, req)
	case "disable":
		e.h.HubCrossTeamDisable(rec, req)
	case "files/enable":
		e.h.HubCrossTeamFilesEnable(rec, req)
	case "files/disable":
		e.h.HubCrossTeamFilesDisable(rec, req)
	default:
		e.t.Fatalf("no such route %q", route)
	}
	return rec
}

func (e *hubSwitchEnv) settings(team string) board.BoardSettings {
	e.t.Helper()
	s, err := e.bs.GetBoardSettings(context.Background(), team)
	require.NoError(e.t, err)
	return s
}

func (e *hubSwitchEnv) notices(team string) []string {
	e.t.Helper()
	msgs, err := e.bs.ListMessages(context.Background(), team, 100, 0, 0)
	require.NoError(e.t, err)
	var out []string
	for _, m := range msgs {
		if m.Origin != nil && *m.Origin == board.OriginHubNotice {
			out = append(out, m.Content)
		}
	}
	return out
}

func (e *hubSwitchEnv) actions() []board.HubOperatorAction {
	e.t.Helper()
	a, err := e.bs.RecentHubOperatorActions(context.Background(), 100)
	require.NoError(e.t, err)
	return a
}

func TestTheHubOwnerFlipsBothSwitchesOnAndOffRecordedAndAnnounced(t *testing.T) {
	e := newHubSwitchEnv(t)
	owner := ownerHeaders(ownerLabel)
	steps := []struct {
		route  string
		msg, f bool
		notice string
	}{
		{"enable", true, false, "Coral Hub's owner (" + ownerLabel + ") turned cross-team messaging on for this team. See Team settings > Messaging."},
		{"files/enable", true, true, "Coral Hub's owner (" + ownerLabel + ") turned file sharing with other teams on for this team. See Team settings > Messaging."},
		{"files/disable", true, false, "Coral Hub's owner (" + ownerLabel + ") turned file sharing with other teams off for this team. See Team settings > Messaging."},
		{"disable", false, false, "Coral Hub's owner (" + ownerLabel + ") turned cross-team messaging off for this team. See Team settings > Messaging."},
	}
	for i, st := range steps {
		rec := e.call("team", st.route, "", owner)
		require.Equal(t, http.StatusOK, rec.Code, st.route+": "+rec.Body.String())
		s := e.settings("team")
		assert.Equal(t, st.msg, s.CrossTeamMessaging, st.route)
		assert.Equal(t, st.f, s.CrossTeamArtifacts, st.route)
		n := e.notices("team")
		require.Len(t, n, i+1, st.route)
		assert.Equal(t, st.notice, n[i], st.route)
		a := e.actions()
		require.Len(t, a, i+1, st.route)
		assert.Equal(t, ownerLabel, a[0].HubUser)
		assert.Equal(t, "team", a[0].Project)
		assert.Equal(t, "/api/board/team/cross-team/"+st.route, a[0].Path)
	}
	assert.Empty(t, e.notices("other"), "another team is not announced to")
	last, err := e.bs.LastSwitchChanges(context.Background(), "team")
	require.NoError(t, err)
	assert.Equal(t, board.SwitchViaHub, last[board.SwitchMessaging].Via)
	assert.Equal(t, ownerLabel, last[board.SwitchMessaging].Who)
	assert.True(t, last[board.SwitchMessaging].Old)
	assert.False(t, last[board.SwitchMessaging].New)
}

func TestARequestForTheValueItAlreadyHasIsAnnouncedToo(t *testing.T) {
	e := newHubSwitchEnv(t)
	require.Equal(t, 200, e.call("team", "enable", "", ownerHeaders(ownerLabel)).Code)
	rec := e.call("team", "enable", "", ownerHeaders(ownerLabel))
	require.Equal(t, 200, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, false, out["changed"])
	n := e.notices("team")
	require.Len(t, n, 2)
	assert.Contains(t, n[1], "asked to turn cross-team messaging on")
	assert.Contains(t, n[1], "it already was on")
	assert.Len(t, e.actions(), 2, "recorded too")
}

// Everyone but the account this computer is linked to is refused, and nothing is changed, recorded or announced.
func TestNoOneButTheHubOwnerMayUseTheSwitchRoutes(t *testing.T) {
	e := newHubSwitchEnv(t)
	forgedVia := ownerHeaders(ownerLabel)
	forgedVia[tunnel.ViaHeader] = "1"
	noMarker := ownerHeaders(ownerLabel)
	delete(noMarker, tunnel.HubRoleHeader)
	noUser := ownerHeaders(ownerLabel)
	delete(noUser, tunnel.HubUserHeader)
	for name, hdr := range map[string]map[string]string{
		"this computer, no stamp": nil,
		"a grantee":               granteeHeaders(),
		"a forged stamp":          forgedVia,
		"no owner marker":         noMarker,
		"no hub user":             noUser,
		"another owner label":     ownerHeaders("hub-user-9999ffff"),
	} {
		for _, route := range []string{"enable", "files/enable"} {
			rec := e.call("team", route, "", hdr)
			assert.Equal(t, http.StatusForbidden, rec.Code, name+" "+route)
			assert.Contains(t, rec.Body.String(), "owner_only", name)
		}
	}
	withLabel(t, "", errors.New("db closed"))
	assert.Equal(t, http.StatusForbidden, e.call("team", "enable", "", ownerHeaders(ownerLabel)).Code, "an unreadable record is no owner")
	assert.False(t, e.settings("team").CrossTeamMessaging)
	assert.False(t, e.settings("team").CrossTeamArtifacts)
	assert.Empty(t, e.notices("team"))
	assert.Empty(t, e.actions())
}

// The lock refuses both directions; a lock that cannot be read, or that was never given, refuses too.
func TestTheLockRefusesTheHubInBothDirectionsAndFailsClosed(t *testing.T) {
	e := newHubSwitchEnv(t)
	owner := ownerHeaders(ownerLabel)
	require.Equal(t, 200, e.call("team", "enable", "", owner).Code)
	require.Equal(t, 200, e.call("team", "files/enable", "", owner).Code)
	before := len(e.actions())

	e.lock = false
	for _, route := range []string{"disable", "files/disable", "enable", "files/enable"} {
		rec := e.call("team", route, "", owner)
		assert.Equal(t, http.StatusForbidden, rec.Code, route)
		var refused map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
		assert.Equal(t, "hub_changes_off", refused["code"], route)
		assert.Equal(t, textHubChangesOff, refused["error"], route)
		assert.Contains(t, refused["error"], "Settings > Network and Hub", route)
	}
	s := e.settings("team")
	assert.True(t, s.CrossTeamMessaging && s.CrossTeamArtifacts, "locked: turning OFF is refused too")

	e.lock, e.lockEr = true, errors.New("settings unreadable")
	assert.Equal(t, http.StatusForbidden, e.call("team", "disable", "", owner).Code, "an unreadable lock refuses")
	e.lockEr = nil
	e.h.SetHubCrossTeamLock(nil)
	assert.Equal(t, http.StatusForbidden, e.call("team", "disable", "", owner).Code, "no lock reader refuses")
	assert.True(t, e.settings("team").CrossTeamMessaging)
	assert.Len(t, e.actions(), before, "a refused request records nothing")

	e.h.SetHubCrossTeamLock(func(context.Context) (bool, error) { return true, nil })
	assert.Equal(t, 200, e.call("team", "disable", "", owner).Code, "unlocked again: it works")
}

// A body or a query is refused whole: the only thing these routes do is the one switch their path names.
func TestTheSwitchRoutesTakeNoBodyAndNoQuery(t *testing.T) {
	e := newHubSwitchEnv(t)
	owner := ownerHeaders(ownerLabel)
	for _, body := range []string{
		`{"cross_team_servers":["homelab"]}`,
		`{"cross_team_messaging":true}`,
		`{"hub_cross_team_settings":false}`,
		`{"cross_team_allowed":[]}`,
		`{"cross_team_messaging":true,"cross_team_artifacts":true}`,
		`x`,
		strings.Repeat(" ", 40) + "{}",
	} {
		rec := e.call("team", "enable", body, owner)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	assert.Equal(t, 200, e.call("team", "enable", `{}`, owner).Code, "an empty object is as good as none")
	assert.Equal(t, 200, e.call("team", "disable", " \n", owner).Code)
	req := httptest.NewRequest(http.MethodPost, "/api/board/team/cross-team/enable?cross_team_artifacts=true", nil)
	for k, v := range owner {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "team")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	e.h.HubCrossTeamEnable(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	s := e.settings("team")
	assert.False(t, s.CrossTeamMessaging || s.CrossTeamArtifacts, "nothing partial was applied")
	assert.Len(t, e.actions(), 2, "only the two good requests were recorded")
}

func TestAMissingTeamIsNotFoundAndNothingIsRecorded(t *testing.T) {
	e := newHubSwitchEnv(t)
	rec := e.call("nobody", "enable", "", ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, e.actions())
}

// The change and the Operator-actions row are one transaction: when the row cannot be written the switch stays as it was and nothing is announced.
func TestWhenTheRecordCannotBeWrittenTheSwitchIsUnchanged(t *testing.T) {
	e := newHubSwitchEnv(t)
	raw, err := sqlx.Open("sqlite", e.dbPath+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`DROP TABLE hub_operator_actions`)
	require.NoError(t, err)
	rec := e.call("team", "enable", "", ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "record_failed")
	assert.False(t, e.settings("team").CrossTeamMessaging, "not applied without its record")
	assert.Empty(t, e.notices("team"))
	last, err := e.bs.LastSwitchChanges(context.Background(), "team")
	require.NoError(t, err)
	assert.Empty(t, last, "the switch log row went with it")
}

// 10 requests a minute per team, refusals of the owner included in nothing: only requests that got past the owner check count; another team has its own; the next minute starts again.
func TestTheHubMayChangeATeamTenTimesAMinute(t *testing.T) {
	e := newHubSwitchEnv(t)
	owner := ownerHeaders(ownerLabel)
	for i := 0; i < hubSwitchPerMinute; i++ {
		route := []string{"enable", "disable"}[i%2]
		require.Equal(t, 200, e.call("team", route, "", owner).Code, route)
	}
	rec := e.call("team", "enable", "", owner)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "action", rec.Header().Get("X-Coral-Limit"), "marked like every per-action limit")
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "hub_switch_rate_limited")
	assert.Equal(t, 200, e.call("other", "enable", "", owner).Code, "another team has its own count")
	for i := 0; i < 30; i++ {
		assert.Equal(t, http.StatusForbidden, e.call("team", "enable", "", granteeHeaders()).Code, "a grantee burns nothing of the owner's minute")
	}
	e.advance(time.Minute)
	assert.Equal(t, 200, e.call("team", "enable", "", owner).Code, "the next minute starts again")
}

// What the Messaging tab reads: can_change, owner_path_live, the lock and who changed each switch last (a person here after a hub user is the newer line).
func TestTheStateEndpointServesTheOwnerPathAndTheLastChange(t *testing.T) {
	e := newHubSwitchEnv(t)
	get := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/board/team/cross-team-state", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", "team")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		e.h.GetCrossTeamState(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	st := get()
	assert.Equal(t, true, st["can_change"])
	assert.Equal(t, true, st["owner_path_live"])
	assert.Equal(t, true, st["hub_may_change"])
	assert.Empty(t, st["last_changed"])

	require.Equal(t, 200, e.call("team", "enable", "", ownerHeaders(ownerLabel)).Code)
	last, _ := get()["last_changed"].(map[string]any)
	msg, _ := last["cross_team_messaging"].(map[string]any)
	require.NotNil(t, msg)
	assert.Equal(t, "hub", msg["via"])
	assert.Equal(t, ownerLabel, msg["by"])
	assert.Equal(t, true, msg["on"])
	assert.NotEmpty(t, msg["at"])
	assert.Nil(t, last["cross_team_artifacts"])

	e.lock = false
	assert.Equal(t, false, get()["hub_may_change"])
	e.lockEr = errors.New("unreadable")
	assert.Equal(t, false, get()["hub_may_change"], "a lock that cannot be read reads as off")
	e.lock, e.lockEr = true, nil

	// The owner here turns it off through the settings route: the line now says this computer, and names no hub user.
	body := `{"subscriber_id":"Operator","cross_team_messaging":false}`
	rec := boardCallRaw(t, e.h, http.MethodPut, body, nil)
	_ = rec
	last, _ = get()["last_changed"].(map[string]any)
	msg, _ = last["cross_team_messaging"].(map[string]any)
	require.NotNil(t, msg)
	assert.Equal(t, "local", msg["via"])
	assert.Nil(t, msg["by"])
	assert.Equal(t, false, msg["on"])
}

// The settings route stays local-only for the two switches, for the owner too.
func TestTheSettingsRouteStaysLocalOnlyForTheTwoSwitches(t *testing.T) {
	e := newHubSwitchEnv(t)
	for _, body := range []string{`{"subscriber_id":"Operator","cross_team_messaging":true}`, `{"subscriber_id":"Operator","cross_team_artifacts":true}`} {
		code, _ := boardCall(t, e.h, http.MethodPut, body, ownerHeaders(ownerLabel))
		assert.Equal(t, http.StatusForbidden, code, body)
	}
	s := e.settings("team")
	assert.False(t, s.CrossTeamMessaging || s.CrossTeamArtifacts)
}

func TestTheNoticeIsAFixedTemplateOfTheValidatedHubUserAlone(t *testing.T) {
	want := map[[3]bool]string{
		{false, true, true}:  "Coral Hub's owner (hub-user-1234abcd) turned cross-team messaging on for this team. See Team settings > Messaging.",
		{false, false, true}: "Coral Hub's owner (hub-user-1234abcd) turned cross-team messaging off for this team. See Team settings > Messaging.",
		{true, true, true}:   "Coral Hub's owner (hub-user-1234abcd) turned file sharing with other teams on for this team. See Team settings > Messaging.",
		{false, true, false}: "Coral Hub's owner (hub-user-1234abcd) asked to turn cross-team messaging on for this team; it already was on. See Team settings > Messaging.",
	}
	for k, w := range want {
		assert.Equal(t, w, textHubSwitchNotice("hub-user-1234abcd", k[0], k[1], k[2]))
	}
	_ = cloud.ViaSecret
}

// The owner's switch routes name no actor and are not a use of the Operator identity: "Don't let Coral Hub act as the Operator" does not govern them (their own lock does), and the guard does
// not record a second Operator-actions row for them. Any other board write through the same guard is still the Operator's and still refused when that switch is on.
func TestTheOperatorGuardLeavesTheOwnerSwitchRoutesToTheirOwnLock(t *testing.T) {
	e := newHubSwitchEnv(t)
	r := chi.NewRouter()
	r.With(TunnelIdentityGuard(e.bs, func(*http.Request) bool { return false })).Post("/api/board/{project}/cross-team/enable", e.h.HubCrossTeamEnable)
	r.With(TunnelIdentityGuard(e.bs, func(*http.Request) bool { return false })).Post("/api/board/{project}/pause", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		for k, v := range ownerHeaders(ownerLabel) {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusForbidden, do("/api/board/team/pause").Code, "an ordinary board write is the Operator's: refused while that switch is on")
	assert.Equal(t, http.StatusOK, do("/api/board/team/cross-team/enable").Code)
	assert.True(t, e.settings("team").CrossTeamMessaging)
	assert.Len(t, e.actions(), 1, "one Operator-actions row, the transaction's")
}
