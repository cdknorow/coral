package routes

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6357 stage 4 (#6459): comments across versions. A comment belongs to its version; reading a later version lists it with where its text stands there.

const carrySeries = "5e7d5e7d5e7d5e7d5e7d5e7d5e7d5e7d"

// twoVersions makes cmtDoc (v1) and cmtPic2 (v2) a series with these texts.
func (rig *cmtRig) twoVersions(v1, v2 string) {
	rig.t.Helper()
	dir := rig.h.artifactDir()
	require.NoError(rig.t, os.WriteFile(filepath.Join(dir, cmtDoc), []byte(v1), 0o644))
	require.NoError(rig.t, os.WriteFile(filepath.Join(dir, cmtPic2), []byte(v2), 0o644))
	require.NoError(rig.t, writeSeriesIndex(rig.h.cfg.CoralDir(), seriesIndex{ID: carrySeries, Board: "team-a", Versions: []seriesVersionRow{{Digest: cmtDoc, N: 1, Session: "a1"}, {Digest: cmtPic2, N: 2, Session: "a1"}}}))
	stampTestRecord(rig.t, rig.h.cfg.CoralDir(), "a1", cmtDoc, seriesStamp{ID: carrySeries, Version: 1})
	stampTestRecord(rig.t, rig.h.cfg.CoralDir(), "a1", cmtPic2, seriesStamp{ID: carrySeries, Version: 2, Previous: cmtDoc})
}

// carriedOn reads v2 with ?series=1 and returns the comments from earlier versions.
func (rig *cmtRig) carriedOn() []map[string]any {
	rig.t.Helper()
	code, out := rig.do("GET", cp(cmtPic2)+"?series=1", cmtAsOwner, nil)
	require.Equal(rig.t, 200, code, fmt.Sprint(out))
	var res []map[string]any
	for _, c := range out["comments"].([]any) {
		if m := c.(map[string]any); m["from_version"] != nil {
			res = append(res, m)
		}
	}
	return res
}

func commentOnQuote(rig *cmtRig, id, quote, prefix, suffix string) int64 {
	code, out := rig.do("POST", cp(id), cmtAsOwner, map[string]any{"body": "about " + quote, "anchor": map[string]any{"quote": quote, "prefix": prefix, "suffix": suffix, "start_line": 2, "end_line": 2}, "board": "team-a"})
	require.Equal(rig.t, http.StatusCreated, code, fmt.Sprint(out))
	return int64(out["id"].(float64))
}

func TestACommentWhoseTextIsStillThereIsCarriedWhereTheTextNowIs(t *testing.T) {
	rig := newCmtRig(t)
	cid := commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	// the text moved down two lines, and was reworded around
	rig.twoVersions("intro\ntake the lock first\nend\n", "new heading\nmore\nintro again\ntake the lock first, then go\nend\n")
	c := rig.carriedOn()
	require.Len(t, c, 1)
	assert.Equal(t, float64(cid), c[0]["id"])
	assert.Equal(t, cmtDoc, c[0]["artifact_id"], "it stays the comment of the version it was written on")
	assert.Equal(t, float64(1), c[0]["from_version"])
	assert.Equal(t, "found", c[0]["carried"])
	assert.Equal(t, float64(4), c[0]["carried_anchor"].(map[string]any)["start_line"], "where it is now, found and not guessed")
	assert.Equal(t, "the lock", c[0]["anchor"].(map[string]any)["quote"], "its own anchor is what was written")
}

func TestACommentWhoseTextIsGoneStaysOnItsOldVersionAndIsSaidSo(t *testing.T) {
	rig := newCmtRig(t)
	commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	rig.twoVersions("intro\ntake the lock first\nend\n", "intro\ntake the mutex first\nend\n") // edited
	c := rig.carriedOn()
	require.Len(t, c, 1)
	assert.Equal(t, "gone", c[0]["carried"])
	assert.Nil(t, c[0]["carried_anchor"], "never pinned to a spot")
	assert.Equal(t, "the lock", c[0]["anchor"].(map[string]any)["quote"], "with its original quote")
	// and a deleted passage
	rig2 := newCmtRig(t)
	commentOnQuote(rig2, cmtDoc, "the lock", "take ", " first")
	rig2.twoVersions("intro\ntake the lock first\nend\n", "intro\nend\n")
	assert.Equal(t, "gone", rig2.carriedOn()[0]["carried"])
}

func TestRepeatedTextIsCarriedOnlyWhenItsSurroundingsPickOnePlace(t *testing.T) {
	rig := newCmtRig(t)
	pick := commentOnQuote(rig, cmtDoc, "TODO", "second ", " here")
	lost := commentOnQuote(rig, cmtDoc, "TODO", "was ", " nowhere")
	rig.twoVersions("a\nsecond TODO here\n", "x\nfirst TODO there\ny\nsecond TODO here\nz\n")
	byID := map[float64]map[string]any{}
	for _, c := range rig.carriedOn() {
		byID[c["id"].(float64)] = c
	}
	assert.Equal(t, "found", byID[float64(pick)]["carried"])
	assert.Equal(t, float64(4), byID[float64(pick)]["carried_anchor"].(map[string]any)["start_line"], "the second one, by its surroundings")
	assert.Equal(t, "ambiguous", byID[float64(lost)]["carried"], "twice here and the surroundings pick neither: no guess")
	assert.Nil(t, byID[float64(lost)]["carried_anchor"])
	// the same text twice with the same surroundings is ambiguous too
	rig2 := newCmtRig(t)
	same := commentOnQuote(rig2, cmtDoc, "TODO", "go ", " now")
	rig2.twoVersions("go TODO now\n", "go TODO now\nmid\ngo TODO now\n")
	c := rig2.carriedOn()
	require.Len(t, c, 1)
	assert.Equal(t, float64(same), c[0]["id"])
	assert.Equal(t, "ambiguous", c[0]["carried"])
}

func TestAWholeDocumentCommentAndRepliesFollowTheirThread(t *testing.T) {
	rig := newCmtRig(t)
	code, out := rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "overall", "anchor": map[string]any{"whole": true}, "board": "team-a"})
	require.Equal(t, 201, code, fmt.Sprint(out))
	whole := int64(out["id"].(float64))
	gone := commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	code, _ = rig.do("POST", cp(cmtDoc), cmtAsOwner, map[string]any{"body": "reply to gone", "parent_id": gone})
	require.Equal(t, 201, code)
	rig.twoVersions("take the lock first\n", "different\n")
	states := map[string]int{}
	for _, c := range rig.carriedOn() {
		states[c["carried"].(string)]++
		if c["id"] == float64(whole) {
			assert.Equal(t, "found", c["carried"])
		}
	}
	assert.Equal(t, map[string]int{"found": 1, "gone": 2}, states, "the thread's reply goes where its first comment goes")
}

func TestWithoutTheFlagTheAnswerIsExactlyWhatItWas(t *testing.T) {
	rig := newCmtRig(t)
	commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	rig.twoVersions("take the lock first\n", "take the lock first\nmore\n")
	code, out := rig.do("GET", cp(cmtPic2), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.Empty(t, out["comments"], "the older version's comment is not listed on the newer without the flag")
	code, out = rig.do("GET", cp(cmtDoc), cmtAsOwner, nil)
	require.Equal(t, 200, code)
	c := out["comments"].([]any)[0].(map[string]any)
	for _, k := range []string{"version", "from_version", "carried", "carried_anchor"} {
		_, has := c[k]
		assert.False(t, has, "no %s without the flag", k)
	}
	// an artifact in no series: the flag changes nothing
	code, out = rig.do("GET", cp(cmtPic)+"?series=1", cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.Empty(t, out["comments"])
	// with the flag, this version's own comments carry their version; earlier versions never show later ones
	code, out = rig.do("GET", cp(cmtDoc)+"?series=1", cmtAsOwner, nil)
	require.Equal(t, 200, code)
	c = out["comments"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(1), c["version"])
	assert.Nil(t, c["from_version"])
	rig.ownerCommentOn(cmtPic2, "written on the newer version")
	code, out = rig.do("GET", cp(cmtDoc)+"?series=1", cmtAsOwner, nil)
	require.Equal(t, 200, code)
	assert.Len(t, out["comments"], 1, "an earlier version never lists the comments of a later one")
}

func TestAReplyAnAskAndAResolveStayWithTheVersionTheCommentWasWrittenOn(t *testing.T) {
	rig := newCmtRig(t)
	rig.agent(1, "Dev", false)
	cid := commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	rig.twoVersions("take the lock first\n", "x\ntake the lock first\n")
	// acting on the carried comment through the newer version's address finds nothing
	for name, path := range map[string]string{"reply": "/%d/reply", "send": "/%d/send", "resolve": "/%d/resolve"} {
		who := cmtAsOwner
		if name == "reply" {
			who = asAgentSession(1)
		}
		code, _ := rig.do("POST", cp(cmtPic2)+fmt.Sprintf(path, cid), who, map[string]any{"agents": []string{"Dev"}, "body": "x"})
		assert.Equal(t, http.StatusNotFound, code, name+" on the newer version's address")
	}
	// through its own version it works, and the ask records that version
	code, out := rig.send(cid, "Dev")
	require.Equal(t, 200, code, fmt.Sprint(out))
	ask, found, err := rig.ss.GetArtifactAsk(t.Context(), cid, sendSid(1))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, cmtDoc, ask.ArtifactID, "the open ask stays tied to the version it quoted")
	code, rep := rig.reply(cid, asAgentSession(1), "done")
	assert.Equal(t, 201, code, fmt.Sprint(rep))
	assert.Equal(t, cmtDoc, rep["artifact_id"], "the reply is in the original version's thread")
	// and it shows on v2 as a carried reply of v1
	n := 0
	for _, c := range rig.carriedOn() {
		if c["parent_id"] != nil {
			n++
			assert.Equal(t, "found", c["carried"])
		}
	}
	assert.Equal(t, 1, n)
}

func TestAnAgentSeesCarriedCommentsOnlyOfBoardsItIsOn(t *testing.T) {
	rig := newCmtRig(t)
	commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	rig.twoVersions("take the lock first\n", "take the lock first\nmore\n")
	code, out := rig.do("GET", cp(cmtPic2)+"?series=1", cmtAsAgent, nil)
	if code == http.StatusOK {
		assert.Empty(t, out["comments"], "an agent not on the comment's board sees none of it, carried or not")
	} else {
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, code)
	}
}

func TestAVersionTooLargeToSearchLeavesTheCommentUncheckedNotMoved(t *testing.T) {
	rig := newCmtRig(t)
	commentOnQuote(rig, cmtDoc, "the lock", "take ", " first")
	rig.twoVersions("take the lock first\n", strings.Repeat("take the lock first\n", carryMaxText/20+2))
	c := rig.carriedOn()
	require.Len(t, c, 1)
	assert.Equal(t, "unchecked", c[0]["carried"])
	assert.Nil(t, c[0]["carried_anchor"])
}
