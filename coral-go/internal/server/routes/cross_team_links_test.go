package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5572 patch 2: the cross-team link table Coral Hub's snapshots fill. Nothing here changes who is admitted yet; these tests pin what the table accepts, how it ages and what the door will read.

const (
	linkSessionA = "0123456789abcdef0123456789abcdef"
	linkSessionB = "fedcba9876543210fedcba9876543210"
	srcTeam1     = "b-aaaaaaaaaa"
	srcTeam2     = "b-bbbbbbbbbb"
)

type linksEnv struct {
	t       *testing.T
	h       *BoardHandler
	bs      *board.Store
	dbPath  string
	target  string // a generated board of this computer
	target2 string
	clock   time.Time
	mu      sync.Mutex
	seen    bool
	seenErr error
	own     string
}

func newLinksEnv(t *testing.T) *linksEnv {
	t.Helper()
	e := &linksEnv{t: t, dbPath: t.TempDir() + "/board.db", clock: time.Now(), own: "homelab"}
	bs, err := boardtest.Open(e.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e.bs = bs
	e.h = e.handler(bs)
	b1, err := bs.CreateBoard(context.Background(), "Platform", "Operator")
	require.NoError(t, err)
	b2, err := bs.CreateBoard(context.Background(), "Mobile", "Operator")
	require.NoError(t, err)
	e.target, e.target2 = b1.ID, b2.ID
	return e
}

// handler is a fresh BoardHandler (a new process) over the same store and the same persisted flag.
func (e *linksEnv) handler(bs *board.Store) *BoardHandler {
	h := NewBoardHandler(bs)
	e.t.Cleanup(h.WaitBackground)
	h.linkClock = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.clock }
	h.SetOwnServerID(func(context.Context) (string, error) { return e.own, nil })
	h.SetLinksSeen(
		func(context.Context) (bool, error) { return e.seen, e.seenErr },
		func(context.Context) error { e.seen = true; return nil })
	return h
}

func (e *linksEnv) advance(d time.Duration) { e.mu.Lock(); e.clock = e.clock.Add(d); e.mu.Unlock() }

func stamped(session string, cap bool) map[string]string {
	h := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubLinksHeader: tunnel.HubLinksSnapshot, tunnel.HubLinksSessionHeader: session}
	if cap {
		h[tunnel.HubLinksCapHeader] = "1"
	}
	return h
}

func (e *linksEnv) post(body string, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, tunnel.HubLinksPath, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.PostCrossTeamLinks(rec, req)
	return rec
}

func (e *linksEnv) snapshot(version int64, links ...snapshotLink) string {
	b, _ := json.Marshal(snapshotBody{Version: version, ThisServer: e.own, Links: links})
	return string(b)
}

func (e *linksEnv) link(src, srcTeam, target string) snapshotLink {
	return snapshotLink{SourceServer: src, SourceTeam: srcTeam, TargetTeam: target, SourceLabel: "QA", TargetLabel: "Platform", SourceServerLabel: "Home lab"}
}

func (e *linksEnv) admits(src, srcTeam, target string) bool {
	return e.h.links.admits(src, srcTeam, target, e.h.linkNow())
}

func TestASnapshotFillsTheTableAndTheDoorReadsExactlyTheLinkedTriple(t *testing.T) {
	e := newLinksEnv(t)
	rec := e.post(e.snapshot(5, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	assert.False(t, e.admits("lab3", srcTeam1, e.target), "another server")
	assert.False(t, e.admits("lab2", srcTeam2, e.target), "another team")
	assert.False(t, e.admits("lab2", srcTeam1, e.target2), "another target: the link is to one team")
	assert.False(t, e.admits("lab2", "QA", e.target), "a name is not an id")
	// The panel's copy is kept too.
	stored, err := e.bs.TeamLinksTo(context.Background(), e.target)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "Home lab", stored[0].SourceServerLabel)
}

func TestASnapshotReplacesTheWholeTable(t *testing.T) {
	e := newLinksEnv(t)
	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target), e.link("lab2", srcTeam2, e.target)), stamped(linkSessionA, true)).Code)
	require.Equal(t, 200, e.post(e.snapshot(2, e.link("lab2", srcTeam2, e.target)), stamped(linkSessionA, true)).Code)
	assert.False(t, e.admits("lab2", srcTeam1, e.target), "a revoked link is gone with the next snapshot")
	assert.True(t, e.admits("lab2", srcTeam2, e.target))
	require.Equal(t, 200, e.post(e.snapshot(3), stamped(linkSessionA, true)).Code)
	assert.False(t, e.admits("lab2", srcTeam2, e.target), "an empty snapshot revokes everything")
	assert.True(t, e.h.links.fresh(e.h.linkNow()), "and is still a snapshot: the table is fresh and empty")
}

// Only Coral Hub, through this process's own connector, on a connection that advertised the capability, with a session: everything else is refused whole.
func TestOnlyCoralHubsSnapshotOnAnAdvertisingConnectionIsAccepted(t *testing.T) {
	e := newLinksEnv(t)
	good := e.snapshot(1, e.link("lab2", srcTeam1, e.target))
	forged := stamped(linkSessionA, true)
	forged[tunnel.ViaHeader] = "1"
	noMarker := stamped(linkSessionA, true)
	delete(noMarker, tunnel.HubLinksHeader)
	wrongMarker := stamped(linkSessionA, true)
	wrongMarker[tunnel.HubLinksHeader] = "delta"
	withUser := stamped(linkSessionA, true)
	withUser[tunnel.HubUserHeader] = "hub-user-1234abcd"
	noSession := stamped(linkSessionA, true)
	delete(noSession, tunnel.HubLinksSessionHeader)
	for name, hdr := range map[string]map[string]string{
		"this computer, no stamp": nil,
		"a forged stamp":          forged,
		"no snapshot marker":      noMarker,
		"another marker":          wrongMarker,
		"a hub user on it":        withUser,
		"no session":              noSession,
		"a malformed session":     stamped("not-a-session", true),
		"no capability this time": stamped(linkSessionA, false),
		"a stamp-less capability": {tunnel.HubLinksHeader: tunnel.HubLinksSnapshot, tunnel.HubLinksSessionHeader: linkSessionA, tunnel.HubLinksCapHeader: "1"},
	} {
		rec := e.post(good, hdr)
		assert.GreaterOrEqual(t, rec.Code, 400, name)
		assert.False(t, e.admits("lab2", srcTeam1, e.target), name)
	}
	assert.False(t, e.h.links.everApplied(), "nothing was applied")
	// A repeated marker or session header is refused too.
	req := httptest.NewRequest(http.MethodPost, tunnel.HubLinksPath, strings.NewReader(good))
	for k, v := range stamped(linkSessionA, true) {
		req.Header.Set(k, v)
	}
	req.Header.Add(tunnel.HubLinksSessionHeader, linkSessionB)
	rec := httptest.NewRecorder()
	e.h.PostCrossTeamLinks(rec, req)
	assert.GreaterOrEqual(t, rec.Code, 400)
	assert.False(t, e.h.links.everApplied())
}

func TestASnapshotMustNameThisComputer(t *testing.T) {
	e := newLinksEnv(t)
	body, _ := json.Marshal(snapshotBody{Version: 1, ThisServer: "someone-else", Links: []snapshotLink{e.link("lab2", srcTeam1, e.target)}})
	assert.Equal(t, http.StatusForbidden, e.post(string(body), stamped(linkSessionA, true)).Code)
	assert.False(t, e.h.links.everApplied())
	e.h.SetOwnServerID(func(context.Context) (string, error) { return "", errors.New("no enrolment") })
	assert.GreaterOrEqual(t, e.post(e.snapshot(1), stamped(linkSessionA, true)).Code, 400, "an unreadable id refuses")
	e.h.SetOwnServerID(nil)
	assert.GreaterOrEqual(t, e.post(e.snapshot(1), stamped(linkSessionA, true)).Code, 400)
	assert.False(t, e.h.links.everApplied())
}

func TestBadShapesAndUnknownFieldsAreRefusedOrDropped(t *testing.T) {
	e := newLinksEnv(t)
	hdr := stamped(linkSessionA, true)
	assert.Equal(t, 400, e.post(`{"version":1,"this_server":"homelab","links":[],"extra":1}`, hdr).Code, "an unknown key")
	assert.Equal(t, 400, e.post(e.snapshot(1)+`{}`, hdr).Code, "a second object")
	assert.Equal(t, 400, e.post(`{"version":-1,"this_server":"homelab","links":[]}`, hdr).Code)
	assert.Equal(t, 400, e.post(`nope`, hdr).Code)
	assert.False(t, e.h.links.everApplied())
	// Each entry that is not a generated id of a real board here is dropped and never stored; the good one stays.
	legacy := "sprint" // a board made before ids: keyed by its name, no row in the boards table
	_, err := e.bs.Subscribe(context.Background(), legacy, "Dev", "Developer", "s1", nil, "mentions")
	require.NoError(t, err)
	rec := e.post(e.snapshot(1,
		e.link("lab2", srcTeam1, e.target),
		e.link("lab2", "QA", e.target),            // source team is a name
		e.link("lab2", srcTeam1, legacy),          // target is a legacy name
		e.link("lab2", srcTeam1, "b-zzzzzzzzzz"),  // target is a b- id that is no board here
		e.link("bad server!", srcTeam1, e.target), // not a server id
		e.link("lab2", "b-AAAAAAAAAA", e.target),  // not the generated alphabet
	), hdr)
	require.Equal(t, 200, rec.Code)
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	assert.False(t, e.admits("lab2", "QA", e.target))
	assert.False(t, e.admits("lab2", srcTeam1, legacy))
	assert.False(t, e.admits("lab2", srcTeam1, "b-zzzzzzzzzz"))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.EqualValues(t, 1, out["links"])
	assert.Equal(t, 5, e.h.links.dropped)
	// A board that merely NAMES itself like an id is no team: the name rule refuses it at creation (QA's #3536) and the table never trusts the string.
	assert.NotEmpty(t, e.bs.LabelProblem("b-cccccccccc"), "a name shaped like an id cannot be created")
}

// A snapshot over any cap is refused whole: the table stays as it was and is counted, never truncated.
func TestAnOverLimitSnapshotIsRefusedWholeAndTheTableStays(t *testing.T) {
	e := newLinksEnv(t)
	hdr := stamped(linkSessionA, true)
	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target)), hdr).Code)
	mk := func(n int, target string) []snapshotLink {
		out := make([]snapshotLink, n)
		for i := range out {
			out[i] = e.link(fmt.Sprintf("lab%d", i), srcTeam1, target)
		}
		return out
	}
	manyTeams := func(teams, each int) []snapshotLink { // within every per-team and team cap, over the total
		var out []snapshotLink
		for i := 0; i < teams; i++ {
			for j := 0; j < each; j++ {
				out = append(out, e.link(fmt.Sprintf("lab%d", j), srcTeam1, fmt.Sprintf("b-%010d", i)))
			}
		}
		return out
	}
	teams := make([]snapshotLink, 0, maxLinkTeams+1)
	for i := 0; i <= maxLinkTeams; i++ {
		teams = append(teams, e.link("lab2", srcTeam1, fmt.Sprintf("b-%010d", i)))
	}
	for name, body := range map[string]string{
		"too many links to one team": e.snapshot(2, mk(maxLinksPerTeam+1, e.target)...),
		"too many teams":             e.snapshot(2, teams...),
		"too many links":             e.snapshot(2, manyTeams(maxLinkTeams, maxSnapshotLinks/maxLinkTeams+1)...),
		"too many bytes":             `{"version":2,"this_server":"homelab","links":[],"pad":"` + strings.Repeat("x", maxSnapshotBytes) + `"}`,
	} {
		rec := e.post(body, hdr)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, name)
		assert.True(t, e.admits("lab2", srcTeam1, e.target), name+": the table is as it was")
	}
	assert.Equal(t, 4, e.h.links.rejected)
	// Exactly at the limit is fine.
	assert.Equal(t, 200, e.post(e.snapshot(2, mk(maxLinksPerTeam, e.target)...), hdr).Code)
}

func TestVersionsWithinASessionAndAcrossSessions(t *testing.T) {
	e := newLinksEnv(t)
	a := func(version int64, session string, srcTeam string) bool {
		rec := e.post(e.snapshot(version, e.link("lab2", srcTeam, e.target)), stamped(session, true))
		require.Equal(t, 200, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out["applied"] == true
	}
	assert.True(t, a(10, linkSessionA, srcTeam1), "the first snapshot of a session")
	assert.False(t, a(9, linkSessionA, srcTeam2), "an older one in the same session is ignored")
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	assert.False(t, e.admits("lab2", srcTeam2, e.target))
	assert.True(t, a(10, linkSessionA, srcTeam1), "an equal version refreshes (the hub re-sends on a timer)")
	assert.True(t, a(11, linkSessionA, srcTeam2), "a newer one replaces")
	assert.True(t, a(3, linkSessionB, srcTeam1), "a LOWER version as the first of a new session is accepted (a hub restore)")
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	assert.False(t, a(2, linkSessionB, srcTeam2), "and then the session's own order holds")
	assert.Equal(t, 2, e.h.links.ignored)
}

func TestAStaleSnapshotAdmitsNothingAndNeitherDoesARestartNorAClockThatMovedBack(t *testing.T) {
	e := newLinksEnv(t)
	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true)).Code)
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	e.advance(linkTTL - time.Second)
	assert.True(t, e.admits("lab2", srcTeam1, e.target), "just inside the TTL")
	e.advance(2 * time.Second)
	assert.False(t, e.admits("lab2", srcTeam1, e.target), "past the TTL")
	assert.False(t, e.h.links.fresh(e.h.linkNow()))
	// A fresh snapshot freshens it again.
	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true)).Code)
	assert.True(t, e.admits("lab2", srcTeam1, e.target))
	// The clock moved BACK: not fresh (never admitted on a reading from before the snapshot).
	e.advance(-time.Hour)
	assert.False(t, e.admits("lab2", srcTeam1, e.target))
	e.advance(time.Hour)
	// A restart: the stored rows are still there for the panel, and admit nothing until a snapshot arrives.
	stored, err := e.bs.TeamLinksTo(context.Background(), e.target)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	restarted := e.handler(e.bs)
	assert.False(t, restarted.links.admits("lab2", srcTeam1, e.target, restarted.linkNow()), "a persisted row never makes a link fresh")
	assert.False(t, restarted.links.fresh(restarted.linkNow()))
}

func TestTheDoorModeNeverGoesBackToTheOldPathAfterTheHubAdvertisedLinks(t *testing.T) {
	e := newLinksEnv(t)
	relayed := func(h *BoardHandler, cap bool, stamp bool) doorMode {
		req := httptest.NewRequest(http.MethodPost, "/api/board/x/external-messages", nil)
		if stamp {
			req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		}
		if cap {
			req.Header.Set(tunnel.HubLinksCapHeader, "1")
		}
		return h.DoorMode(context.Background(), req)
	}
	assert.Equal(t, doorOld, relayed(e.h, false, true), "a hub that never advertised it: today's path")
	assert.False(t, e.seen)
	assert.Equal(t, doorOld, relayed(e.h, true, false), "the capability header with no per-process stamp is not trusted")
	assert.Equal(t, doorLinks, relayed(e.h, true, true))
	assert.True(t, e.seen, "advertised once: remembered")
	assert.Equal(t, doorRefuse, relayed(e.h, false, true), "a reconnect to a hub without it: refused, not the old path")
	// Also after a restart (the flag is persisted, the table is not).
	restarted := e.handler(e.bs)
	assert.Equal(t, doorRefuse, relayed(restarted, false, true))
	assert.Equal(t, doorLinks, relayed(restarted, true, true))
	// An unreadable flag reads as seen: the strict side.
	e.seen, e.seenErr = false, errors.New("settings unreadable")
	assert.Equal(t, doorRefuse, relayed(e.handler(e.bs), false, true))
}

func TestADowngradeAfterASnapshotWasAppliedRefusesEvenWithTheFlagLost(t *testing.T) {
	e := newLinksEnv(t)
	e.h.SetLinksSeen(func(context.Context) (bool, error) { return false, nil }, func(context.Context) error { return nil }) // a flag that does not stick
	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true)).Code)
	req := httptest.NewRequest(http.MethodPost, "/api/board/x/external-messages", nil)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	assert.Equal(t, doorRefuse, e.h.DoorMode(context.Background(), req), "applied in this process: no capability now means refuse")
}

func TestTheHubsLabelsAreCleanedAndNeverTrusted(t *testing.T) {
	e := newLinksEnv(t)
	l := e.link("lab2", srcTeam1, e.target)
	l.SourceLabel = "Pay\u202eroll\u200b team\n" + strings.Repeat("x", 500)
	l.SourceServerLabel = "\u0007Home\u2066 lab"
	require.Equal(t, 200, e.post(e.snapshot(1, l), stamped(linkSessionA, true)).Code)
	stored, err := e.bs.TeamLinksTo(context.Background(), e.target)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	for _, s := range []string{stored[0].SourceLabel, stored[0].SourceServerLabel} {
		for _, r := range s {
			assert.False(t, r < 0x20 || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069), "no control or bidi character in %q", s)
		}
	}
	assert.LessOrEqual(t, len([]rune(stored[0].SourceLabel)), 120)
}

func TestTheOwnersStateShowsTheLinksTheirFreshnessAndTheOldPathUntilAHubAdvertises(t *testing.T) {
	e := newLinksEnv(t)
	get := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/board/"+e.target+"/cross-team-state", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", e.target)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		e.h.GetCrossTeamState(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	_, err := e.bs.UpdateBoardSettings(context.Background(), e.target, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true), CrossTeamServers: &[]string{"lab9"}})
	require.NoError(t, err)
	st := get()
	links, _ := st["links"].([]any)
	require.Len(t, links, 1, "no hub has advertised links: the old list of servers")
	assert.Equal(t, "lab9", links[0].(map[string]any)["server_id"])

	require.Equal(t, 200, e.post(e.snapshot(1, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true)).Code)
	st = get()
	links, _ = st["links"].([]any)
	require.Len(t, links, 1)
	row := links[0].(map[string]any)
	assert.Equal(t, "lab2", row["server_id"], "from the snapshot now, not the old list")
	assert.Equal(t, "Home lab", row["server_label"])
	assert.Equal(t, srcTeam1, row["team_id"])
	assert.Equal(t, statusReady, st["status"].(map[string]any)["state"])

	e.advance(linkTTL + time.Minute)
	st = get()
	assert.Equal(t, statusStale, st["status"].(map[string]any)["state"], "past the TTL: stale, and the last known rows are still shown")
	links, _ = st["links"].([]any)
	assert.Len(t, links, 1)
}

// #6377 (coral-hub's ask): the owner's status says "stale" only when an old table PAUSED something. A last applied table with no link in it admitted nothing while fresh either, so past the TTL the team is
// "waiting" (for a link), not stale; a non-empty old table stays stale; a process with no table yet (a hub was seen before) stays stale.
func TestAnOldEmptyLinkTableIsWaitingNotStale(t *testing.T) {
	e := newLinksEnv(t)
	state := func(h *BoardHandler) string {
		req := httptest.NewRequest(http.MethodGet, "/api/board/"+e.target+"/cross-team-state", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", e.target)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h.GetCrossTeamState(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out["status"].(map[string]any)["state"].(string)
	}
	_, err := e.bs.UpdateBoardSettings(context.Background(), e.target, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true)})
	require.NoError(t, err)

	// empty and old: waiting, fresh or not
	require.Equal(t, 200, e.post(e.snapshot(1), stamped(linkSessionA, true)).Code)
	assert.Equal(t, statusWaiting, state(e.h), "an empty table, fresh")
	e.advance(linkTTL + time.Hour)
	assert.Equal(t, statusWaiting, state(e.h), "an empty table past the TTL admitted nothing before either: waiting, not stale")

	// a table with a link in it, old: stale (the link it held is paused)
	require.Equal(t, 200, e.post(e.snapshot(2, e.link("lab2", srcTeam1, e.target)), stamped(linkSessionA, true)).Code)
	assert.Equal(t, statusReady, state(e.h))
	e.advance(linkTTL + time.Minute)
	assert.Equal(t, statusStale, state(e.h), "a non-empty table past the TTL stays stale")

	// a table that links some OTHER team is non-empty too: the rule is about the table, as the ask says
	require.Equal(t, 200, e.post(e.snapshot(3, e.link("lab2", srcTeam1, e.target2)), stamped(linkSessionA, true)).Code)
	e.advance(linkTTL + time.Minute)
	assert.Equal(t, statusStale, state(e.h), "non-empty for another team: still stale")

	// none yet in this process (a restart) after a hub was seen: stale, as before
	e.seen = true
	fresh := e.handler(e.bs)
	assert.Equal(t, statusStale, state(fresh), "no snapshot applied since this process started: stale")
}

// Labels are optional display text (an empty one is fine and decides nothing), and the version is a whole int64 (Coral Hub sends milliseconds, about 1.8e12).
func TestLabelsAreOptionalAndTheVersionIsAWholeInt64(t *testing.T) {
	e := newLinksEnv(t)
	hdr := stamped(linkSessionA, true)
	body := fmt.Sprintf(`{"version":1800000000123,"this_server":"homelab","links":[{"source_server":"lab2","source_team":%q,"target_team":%q}]}`, srcTeam1, e.target)
	require.Equal(t, 200, e.post(body, hdr).Code)
	assert.True(t, e.admits("lab2", srcTeam1, e.target), "no labels at all")
	stored, err := e.bs.TeamLinksTo(context.Background(), e.target)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Empty(t, stored[0].SourceLabel)
	assert.Empty(t, stored[0].TargetLabel)
	older := fmt.Sprintf(`{"version":1800000000122,"this_server":"homelab","links":[]}`)
	rec := e.post(older, hdr)
	require.Equal(t, 200, rec.Code)
	assert.True(t, e.admits("lab2", srcTeam1, e.target), "an older millisecond version in the session is ignored")
	assert.Equal(t, 400, e.post(`{"version":1.8e12,"this_server":"homelab","links":[]}`, hdr).Code, "a float is not a version")
	assert.Equal(t, 400, e.post(`{"version":"1800000000123","this_server":"homelab","links":[]}`, hdr).Code, "nor is a string")
}

// A snapshot may also carry the links where a team HERE is the source: kept to show "this team can message X", never admitting anything.
func TestLinksWhereThisComputerIsTheSourceAreDisplayOnly(t *testing.T) {
	e := newLinksEnv(t)
	hdr := stamped(linkSessionA, true)
	out := snapshotLink{SourceServer: "homelab", SourceTeam: e.target, TargetServer: "lab2", TargetTeam: srcTeam1, TargetLabel: "Their QA"}
	neither := snapshotLink{SourceServer: "lab3", SourceTeam: srcTeam2, TargetServer: "lab2", TargetTeam: srcTeam1} // not about this computer at all
	in := e.link("lab2", srcTeam1, e.target)
	rec := e.post(e.snapshot(1, in, out, neither), hdr)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.True(t, e.admits("lab2", srcTeam1, e.target), "the link TO this team admits")
	assert.False(t, e.admits("homelab", e.target, srcTeam1), "the link FROM this team admits nothing here")
	assert.False(t, e.admits("lab2", srcTeam1, srcTeam1))
	assert.Equal(t, 1, e.h.links.dropped, "a link between two other computers is dropped")
	to, err := e.bs.TeamLinksTo(context.Background(), e.target)
	require.NoError(t, err)
	assert.Len(t, to, 1)
	from, err := e.bs.TeamLinksFrom(context.Background(), e.target)
	require.NoError(t, err)
	require.Len(t, from, 1)
	assert.Equal(t, "lab2", from[0].TargetServer)
	assert.Equal(t, srcTeam1, from[0].TargetTeam)
	// Saying "this computer" out loud as the target server is the same as leaving it out.
	explicit := snapshotLink{SourceServer: "lab2", SourceTeam: srcTeam2, TargetServer: "HOMELAB", TargetTeam: e.target}
	require.Equal(t, 200, e.post(e.snapshot(2, in, explicit), hdr).Code)
	assert.True(t, e.admits("lab2", srcTeam2, e.target))
}
