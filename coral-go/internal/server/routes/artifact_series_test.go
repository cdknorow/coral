package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6357 stage 1: artifact series, local routes only. The ownerRig has a1 and a2 on team-a, b1 on team-b, o1 on team-o and n1 on no team.

func (rig *artifactRig) uploadVersion(session, name, text, ref string) (int, map[string]any) {
	req, err := http.NewRequest(http.MethodPost, rig.srv.URL+"/api/agent/artifacts?session_id="+session, strings.NewReader(text))
	require.NoError(rig.t, err)
	req.Header.Set("X-Artifact-Name", name)
	req.Header.Set("X-Artifact-Media-Type", "text/plain")
	if ref != "" {
		req.Header.Set(seriesHeader, ref)
	}
	resp, err := agentClient.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// noFollow asks without following a redirect: as session's agent, or as the dashboard when session is "".
func (rig *artifactRig) noFollow(session, method, route string) (*http.Response, string) {
	req, err := http.NewRequest(method, rig.srv.URL+route, nil)
	require.NoError(rig.t, err)
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if session == "" {
		req.Header.Set(testDashboardHeader, "1")
	} else {
		req.URL.RawQuery = "session_id=" + session
		cli = agentClient
		old := cli.CheckRedirect
		defer func() { cli.CheckRedirect = old }()
		cli.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := cli.Do(req)
	require.NoError(rig.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// seriesOf makes a series on team-a: v1 by a1, v2 by a1 as a new version of v1, v3 by a2 as a new version of the series. It returns the series id and the three digests.
func seriesOf(t *testing.T, rig *artifactRig) (string, [3]string) {
	t.Helper()
	v1 := rig.uploadText("a1", "plan.txt", "plan, first draft")
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, second draft", "coral://artifacts/"+v1)
	require.Equal(t, http.StatusCreated, code, out)
	sid, _ := out["series_id"].(string)
	require.Regexp(t, `^[0-9a-f]{32}$`, sid)
	assert.EqualValues(t, 2, out["version"])
	assert.EqualValues(t, 2, out["versions"])
	v2 := strings.TrimPrefix(out["uri"].(string), "coral://artifacts/")
	code, out = rig.uploadVersion("a2", "plan.txt", "plan, third draft", "coral://artifacts/series/"+sid)
	require.Equal(t, http.StatusCreated, code, out)
	assert.EqualValues(t, 3, out["version"])
	assert.Equal(t, sid, out["series_id"])
	v3 := strings.TrimPrefix(out["uri"].(string), "coral://artifacts/")
	return sid, [3]string{v1, v2, v3}
}

func TestANewVersionMakesASeriesAndEachVersionKeepsItsOwnDigest(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	assert.NotEqual(t, d[0], d[1])
	for i, digest := range d {
		rec := recordsOfDigest(rig.coralDir, digest)
		require.Len(t, rec, 1)
		assert.Equal(t, sid, rec[0].rec.SeriesID, "the record carries the series")
		assert.Equal(t, i+1, rec[0].rec.Version)
	}
	rec3 := recordsOfDigest(rig.coralDir, d[2])[0].rec
	assert.Equal(t, d[1], rec3.Previous, "version 3 says which one is before it")
	// the sidecar is the first writer's and never carries series fields (it is shared by every upload of the same bytes)
	raw, err := os.ReadFile(filepath.Join(rig.coralDir, "artifacts", "objects", d[1]+".json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), sid)
	// each digest still opens exactly its own bytes
	for i, text := range []string{"plan, first draft", "plan, second draft", "plan, third draft"} {
		status, body := rig.getAs("a1", "/api/artifacts/"+d[i]+"?v=2")
		assert.Equal(t, 200, status)
		assert.Equal(t, text, body)
	}
}

func TestTheSameBytesAsTheNewestAddNothingAndAsAnOlderOneAreRefusedByName(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, third draft", "coral://artifacts/series/"+sid)
	assert.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["same"])
	assert.EqualValues(t, 3, out["version"], "the newest version is the answer")
	assert.Equal(t, "coral://artifacts/"+d[2], out["uri"])
	assert.Len(t, seriesMembers(rig.coralDir, sid), 3, "nothing was added")

	code, out = rig.uploadVersion("a1", "plan.txt", "plan, first draft", "coral://artifacts/series/"+sid)
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Equal(t, "same_as_older_version", out["code"])
	assert.Contains(t, fmt.Sprint(out["message"]), "version 1", "it names the version the bytes are, and the newest")
	assert.Contains(t, fmt.Sprint(out["message"]), "version 3")
	assert.Len(t, seriesMembers(rig.coralDir, sid), 3)
}

func TestOnlyTheBoardThatUploadedAnArtifactCanAddAVersionOfIt(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	for name, ref := range map[string]string{"its digest": "coral://artifacts/" + d[0], "its series": "coral://artifacts/series/" + sid} {
		code, out := rig.uploadVersion("b1", "x.txt", "bob's version of alice's plan "+name, ref)
		assert.Equal(t, http.StatusNotFound, code, name+": %v", out)
		assert.Equal(t, "series_not_found", out["code"], name)
		assert.NotContains(t, fmt.Sprint(out), sid, name+": the refusal names nothing of the series")
	}
	// an artifact that is in no series yet: another board cannot start one from it either
	solo := rig.uploadText("a1", "solo.txt", "alice's artifact, never versioned")
	code, out := rig.uploadVersion("b1", "x.txt", "bob wants to version alice's solo", "coral://artifacts/"+solo)
	assert.Equal(t, http.StatusNotFound, code, out)
	_, _, in := digestSeries(rig.coralDir, solo)
	assert.False(t, in, "and no series was started from it")
	code, out = rig.uploadVersion("n1", "x.txt", "an agent on no board", "coral://artifacts/"+d[0])
	assert.Equal(t, http.StatusForbidden, code, out)
	assert.Len(t, seriesMembers(rig.coralDir, sid), 3, "nothing was added by either")
	// a made-up series id and a digest nobody uploaded look the same
	code, out = rig.uploadVersion("a1", "x.txt", "whatever", "coral://artifacts/series/"+strings.Repeat("ab", 16))
	assert.Equal(t, http.StatusNotFound, code, out)
	code, out = rig.uploadVersion("a1", "x.txt", "whatever", "coral://artifacts/"+strings.Repeat("cd", 32))
	assert.Equal(t, http.StatusNotFound, code, out)
}

func TestTheReferenceIsExactlyADigestOrASeriesLinkAndNothingIsLookedUpForAnythingElse(t *testing.T) {
	rig, _ := ownerRig(t)
	for _, ref := range []string{"../../etc/passwd", "sha256:" + strings.Repeat("a", 64), strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 33), "coral://artifacts/series/" + strings.Repeat("A", 32), "coral://artifacts/series/../x", "series/" + strings.Repeat("a", 31) + "/", "a b"} {
		code, out := rig.uploadVersion("a1", "x.txt", "text for "+ref, ref)
		assert.Equal(t, http.StatusBadRequest, code, "%q: %v", ref, out)
	}
}

func TestBytesThatBelongToAnotherSeriesAreRefusedWithoutSayingWhichOrWhose(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, _ := seriesOf(t, rig) // team-a's series holds "plan, second draft"
	own := rig.uploadText("b1", "b.txt", "bob's own artifact")
	code, out := rig.uploadVersion("b1", "b2.txt", "plan, second draft", "coral://artifacts/"+own)
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Equal(t, "these bytes already belong to another series", out["message"])
	for _, secret := range []string{sid, "team-a", "alice", "a1", "a2", "version"} {
		assert.NotContains(t, strings.ToLower(fmt.Sprint(out)), secret, "the answer names nothing of the other series: %q", secret)
	}
	assert.Empty(t, seriesMembers(rig.coralDir, ""), "no series id, no members")
	assert.Len(t, seriesMemberSet(rig.coralDir), 3, "and no series was made for bob: only team-a's three versions are in one")
}

func TestASeriesHoldsAtMostTheCap(t *testing.T) {
	rig, _ := ownerRig(t)
	prev := seriesVersionCap
	seriesVersionCap = 3
	t.Cleanup(func() { seriesVersionCap = prev })
	sid, _ := seriesOf(t, rig) // three versions: full
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, fourth draft", "coral://artifacts/series/"+sid)
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Equal(t, "series_full", out["code"])
	assert.Len(t, seriesMembers(rig.coralDir, sid), 3)
}

func TestTwoAgentsAddingVersionsAtOnceGetDistinctNumbers(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, _ := seriesOf(t, rig)
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[int]bool{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := []string{"a1", "a2"}[i%2]
			code, out := rig.uploadVersion(who, "plan.txt", fmt.Sprintf("a concurrent draft %d", i), "coral://artifacts/series/"+sid)
			if code != http.StatusCreated {
				t.Errorf("upload %d: %d %v", i, code, out)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			n := int(out["version"].(float64))
			if got[n] {
				t.Errorf("version %d given twice", n)
			}
			got[n] = true
		}()
	}
	wg.Wait()
	ms := seriesMembers(rig.coralDir, sid)
	require.Len(t, ms, 15, "three and twelve")
	for i, m := range ms {
		assert.Equal(t, i+1, m.N, "numbers are consecutive, none twice")
	}
	idx, ok := readSeriesIndex(rig.coralDir, sid)
	require.True(t, ok)
	assert.Len(t, idx.Versions, 15, "the index agrees")
}

func TestTheSeriesLinkRedirectsToTheNewestVersionTheCallerCanRead(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	resp, _ := rig.noFollow("a1", http.MethodGet, "/api/artifacts/series/"+sid)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/api/artifacts/"+d[2], resp.Header.Get("Location"), "exactly the digest route: nothing else in it")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	resp, _ = rig.noFollow("", http.MethodGet, "/api/artifacts/series/"+sid)
	assert.Equal(t, http.StatusFound, resp.StatusCode, "the dashboard too")
	// another team's agent can read none of it: the same 404 as no such series
	resp, _ = rig.noFollow("b1", http.MethodGet, "/api/artifacts/series/"+sid)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = rig.noFollow("b1", http.MethodGet, "/api/artifacts/series/"+strings.Repeat("ab", 16))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = rig.noFollow("a1", http.MethodGet, "/api/artifacts/series/not-a-series")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTheVersionListHasFixedFieldsAndOnlyWhatTheCallerMayRead(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	for _, route := range []string{"/api/artifacts/series/" + sid + "/versions", "/api/artifacts/" + d[1] + "/versions"} {
		status, body := rig.getAs("a2", route)
		require.Equal(t, 200, status, route)
		var rows []map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &rows))
		require.Len(t, rows, 3, route)
		for i, row := range rows {
			assert.Equal(t, d[i], row["digest"])
			assert.EqualValues(t, i+1, row["version"])
			assert.EqualValues(t, 3, row["versions"])
			assert.Equal(t, i == 2, row["is_latest"])
			assert.Equal(t, "plan.txt", row["name"])
			assert.Equal(t, "text/plain", row["media_type"])
			assert.NotEmpty(t, row["at"])
			assert.ElementsMatch(t, []string{"digest", "version", "versions", "is_latest", "name", "size", "media_type", "at", "uploader", "role"}, keysOf(row), "fixed fields: no series id, no path")
		}
		status, _ = rig.getAs("b1", route)
		assert.Equal(t, 404, status, "another team's agent: the same 404 as no such series: "+route)
	}
	// an artifact in no series has no versions list
	lone := rig.uploadText("a1", "lone.txt", "never versioned")
	status, _ := rig.getAs("a1", "/api/artifacts/"+lone+"/versions")
	assert.Equal(t, 404, status)
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTheListAndTheMetaAddSeriesFieldsOnlyToAVersion(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	lone := rig.uploadText("a1", "lone.txt", "never versioned")
	req, _ := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/artifacts", nil)
	req.Header.Set(testDashboardHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var list struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	b, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(b, &list), string(b))
	byID := map[string]map[string]any{}
	for _, a := range list.Artifacts {
		byID[a["id"].(string)] = a
	}
	for i, digest := range d {
		row := byID[digest]
		require.NotNil(t, row, digest)
		assert.Equal(t, sid, row["series_id"])
		assert.EqualValues(t, i+1, row["version"])
		assert.EqualValues(t, 3, row["versions"])
		assert.Equal(t, i == 2, row["is_latest"])
	}
	for _, k := range []string{"series_id", "version", "versions", "is_latest"} {
		assert.NotContains(t, byID[lone], k, "an unversioned artifact has no series fields")
	}
	status, body := rig.getAs("a1", "/api/artifacts/"+d[1]+"/meta")
	require.Equal(t, 200, status)
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &meta))
	assert.Equal(t, sid, meta["series_id"])
	assert.EqualValues(t, 2, meta["version"])
	assert.Equal(t, false, meta["is_latest"])
	status, body = rig.getAs("a1", "/api/artifacts/"+lone+"/meta")
	require.Equal(t, 200, status)
	assert.NotContains(t, body, "series_id")
}

func TestTheRecordsWinOverTheIndex(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	// a lost index: the series is found again from the records
	require.NoError(t, os.Remove(seriesPath(rig.coralDir, sid)))
	ms := seriesMembers(rig.coralDir, sid)
	require.Len(t, ms, 3)
	assert.Equal(t, d[2], ms[2].Digest)
	// an index that claims a version the records do not have, and a wrong number: the records decide
	idx := seriesIndex{ID: sid, Board: "team-b", Versions: []seriesVersionRow{{Digest: d[0], N: 9, Session: "a1"}, {Digest: strings.Repeat("ef", 32), N: 4, Session: "a1"}}}
	require.NoError(t, writeSeriesIndex(rig.coralDir, idx))
	ms = seriesMembers(rig.coralDir, sid)
	require.Len(t, ms, 3, "the stale entries are dropped, the real versions found")
	for i, m := range ms {
		assert.Equal(t, i+1, m.N)
		assert.Equal(t, "team-a", m.Board, "owner and uploader come from the records, not the index")
	}
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, fourth draft", "coral://artifacts/series/"+sid)
	assert.Equal(t, http.StatusCreated, code, "the index says team-b owns it; the records say team-a: team-a may add: %v", out)
}

func TestDeletingAVersionLeavesTheOthersTheirNumbersAndASeriesDeleteTakesAllOfThem(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	req, _ := http.NewRequest(http.MethodDelete, rig.srv.URL+"/api/artifacts/"+d[1], nil)
	req.Header.Set(testDashboardHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	status, body := rig.getAs("a1", "/api/artifacts/series/"+sid+"/versions")
	require.Equal(t, 200, status)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &rows))
	require.Len(t, rows, 2)
	assert.EqualValues(t, 1, rows[0]["version"])
	assert.EqualValues(t, 3, rows[1]["version"], "version 3 keeps its number: version 2 is a gap")
	idx, ok := readSeriesIndex(rig.coralDir, sid)
	require.True(t, ok)
	assert.Len(t, idx.Versions, 2, "the index forgot it")
	// a new version after the gap numbers on from the newest
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, fourth draft", "coral://artifacts/series/"+sid)
	require.Equal(t, http.StatusCreated, code, out)
	assert.EqualValues(t, 4, out["version"])

	// the series delete: a person only
	resp, _ = rig.noFollow("a1", http.MethodDelete, "/api/artifacts/series/"+sid)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "an agent's token never deletes a series")
	resp, _ = rig.noFollow("", http.MethodDelete, "/api/artifacts/series/"+sid)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	for _, digest := range []string{d[0], d[2]} {
		status, _ := rig.getAs("", "/api/artifacts/"+digest+"/meta")
		assert.Equal(t, 404, status, "every version went")
	}
	_, statErr := os.Stat(seriesPath(rig.coralDir, sid))
	assert.True(t, os.IsNotExist(statErr), "and the index")
}

func TestEveryMemberOfASeriesIsProtectedFromPruning(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	lone := rig.uploadText("a1", "lone.txt", "never versioned")
	prot, err := rig.h.protectedArtifacts(t.Context())
	require.NoError(t, err)
	for _, digest := range d {
		assert.True(t, prot[digest], "a version is protected: %s", digest)
		p, err := rig.h.isProtected(t.Context(), digest)
		require.NoError(t, err)
		assert.True(t, p)
	}
	assert.False(t, prot[lone], "an artifact in no series is not")
	kept, err := rig.h.keptArtifacts(t.Context())
	require.NoError(t, err)
	assert.False(t, kept[d[0]], "the Operator's own keep list is only what it kept")
	_ = sid
}
