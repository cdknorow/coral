package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4441 (Security's #4440 INFO 2): since #4440 a received file waits for its message, and one whose message never comes stays stored and unlisted.
// Those files count toward the received-files cap, Settings lists them, and the Operator may delete them, by exact digest, never automatically.

func hexOf(data []byte) string { return strings.TrimPrefix(sha256Of(data), "sha256:") }

func TestReceivedPendingFilesCountTowardTheCap(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	setReceivedCap(e, 20)
	a, b, c := []byte("aaaaaaaaa\n"), []byte("bbbbbbbbb\n"), []byte("c\n")
	for i, d := range [][]byte{a, b} {
		status, out := e.sendUnder(relaySrc, "m"+string(rune('1'+i)), d)
		require.Equal(t, 200, status, out)
	}
	assert.Empty(t, e.listed(), "no message has come: nothing is listed")
	used, objects, _, unlisted := receivedUsage(e.dir, e.now)
	assert.EqualValues(t, 20, used, "the files waiting for a message are counted")
	assert.Equal(t, 2, objects)
	assert.Empty(t, unlisted, "they are not unlisted yet: their message may still come")

	status, out := e.sendUnder(relaySrc, "m3", c)
	assert.Equal(t, 413, status)
	assert.Equal(t, "store_full", out["error"], "the cap counts them: %v", out)
	status, out = e.sendUnder(relaySrc, "m1", a)
	assert.Equal(t, 200, status, "a file already waiting adds nothing: %v", out)
}

func TestReceivedUnlistedAreTheExpiredOnesNothingLists(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	setReceivedCap(e, 1<<20)
	waiting, late, claimed, uploaded := []byte("still waiting\n"), []byte("never named\n"), []byte("named later\n"), []byte("also uploaded here\n")
	status, out := e.sendUnder(relaySrc, "w", waiting)
	require.Equal(t, 200, status, out)
	for _, c := range []struct {
		msg  string
		data []byte
	}{{"l", late}, {"c", claimed}, {"u", uploaded}} {
		status, out = e.sendUnder(relaySrc, c.msg, c.data)
		require.Equal(t, 200, status, out)
	}
	code, out := e.deliver(relaySrc, "c") // its message arrives in time: listed
	require.Equal(t, 200, code, out)
	_, _, err := storeArtifact(e.dir, "s1", "mine.txt", "text/plain", uploaded) // an agent here uploaded the same bytes
	require.NoError(t, err)

	_, _, _, unlisted := receivedUsage(e.dir, e.now)
	assert.Empty(t, unlisted, "nothing has expired yet")

	// 31 minutes later: the "waiting" file (its message never came) and the "late" one are unlisted; the claimed one is listed on its board and the
	// uploaded one is an agent's upload as well.
	later := e.now.Add(receivedPendingFor + time.Minute)
	used, objects, _, unlisted := receivedUsage(e.dir, later)
	require.Len(t, unlisted, 2, "%v", unlisted)
	ids := []string{unlisted[0].ID, unlisted[1].ID}
	assert.ElementsMatch(t, []string{hexOf(waiting), hexOf(late)}, ids)
	assert.Equal(t, 4, objects, "all four are counted once")
	assert.EqualValues(t, len(waiting)+len(late)+len(claimed)+len(uploaded), used)
	assert.LessOrEqual(t, unlisted[0].At, unlisted[1].At, "oldest first")

	// A fresh pending receipt of the same file (its message may still come) keeps it out of the list until that one expires too.
	e.now = later
	status, out = e.sendUnder(relaySrc, "l2", late)
	require.Equal(t, 200, status, out)
	_, _, _, unlisted = receivedUsage(e.dir, later)
	require.Len(t, unlisted, 1, "only the other file: %v", unlisted)
	assert.Equal(t, hexOf(waiting), unlisted[0].ID)
	_, _, _, unlisted = receivedUsage(e.dir, later.Add(receivedPendingFor+time.Minute))
	assert.Len(t, unlisted, 2, "both again once the new receipt expires")
}

func TestReceivedPendingWithoutItsObjectCountsForNothing(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("deleted meanwhile\n")
	status, out := e.sendUnder(relaySrc, "m1", data)
	require.Equal(t, 200, status, out)
	require.NoError(t, os.Remove(filepath.Join(e.dir, "artifacts", "objects", hexOf(data))))
	used, objects, _, unlisted := receivedUsage(e.dir, e.now.Add(receivedPendingFor+time.Minute))
	assert.EqualValues(t, 0, used)
	assert.Equal(t, 0, objects)
	assert.Empty(t, unlisted)
}

func (rig *artifactRig) keep(id string, header map[string]string) (int, map[string]any) {
	req, err := http.NewRequest(http.MethodPut, rig.srv.URL+"/api/artifacts/"+id+"/keep", nil)
	require.NoError(rig.t, err)
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

// The route, on a rig with real handlers: Operator only, exact digests, and each file re-checked.
func postUnlisted(t *testing.T, rig *artifactRig, header map[string]string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, rig.srv.URL+"/api/artifacts/received/unlisted/delete", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestDeleteUnlistedReceivedFiles(t *testing.T) {
	rig := newArtifactRig(t)
	bh := &BoardHandler{coralDir: rig.coralDir}
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	store := func(text string, session string) string {
		id, _, err := storeArtifact(rig.coralDir, session, "f.txt", "text/plain", []byte(text))
		require.NoError(t, err)
		return id
	}
	pending := func(id, board, at string, size int) {
		require.NoError(t, bh.writePendingReceipt("src-1", "msg-"+id[:8], crossTeamArtifactRecord{ID: id, Name: "f.txt", Size: int64(size), Board: board, OtherBoard: "alpha", Server: "src-1", At: at}))
	}
	exact := store("unlisted and expired", "")
	waiting := store("still waiting for its message", "")
	listed := store("listed on a board", "")
	uploaded := store("an agent uploaded this", "s1")
	kept := store("kept by the Operator", "")
	plain := store("a plain upload, never received", "s1")
	for _, id := range []string{exact, listed, uploaded, kept} {
		pending(id, "beta", old, 10)
	}
	pending(waiting, "beta", recent, 10)
	require.NoError(t, bh.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: listed, Name: "f.txt", Size: 10, Board: "gamma", OtherBoard: "alpha", Server: "src-1"}))
	code, out := rig.keep(kept, map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, code, out)

	// Operator only.
	body := map[string]any{"ids": []string{exact}}
	for name, hdr := range map[string]map[string]string{"an agent's token": {testAgentHeader: "s1"}, "a script": {}, "Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"}} {
		code, out := postUnlisted(t, rig, hdr, body)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "operator_only", out["code"], name)
	}
	_, err := os.Stat(rig.object(exact))
	require.NoError(t, err, "still there after the refusals")

	dash := map[string]string{testDashboardHeader: "1"}
	for name, b := range map[string]any{
		"no ids":       map[string]any{"ids": []string{}},
		"a short id":   map[string]any{"ids": []string{"abc"}},
		"upper case":   map[string]any{"ids": []string{strings.ToUpper(exact)}},
		"not json":     "x",
		"over the cap": map[string]any{"ids": make([]string, maxUnlistedDeleteIDs+1)},
	} {
		code, _ := postUnlisted(t, rig, dash, b)
		assert.Equal(t, http.StatusBadRequest, code, name)
	}

	code, out = postUnlisted(t, rig, dash, map[string]any{"ids": []string{exact, waiting, listed, uploaded, kept, plain, strings.Repeat("0", 64), exact}})
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 1, out["deleted"], "only the exact digest that is unlisted and expired: %v", out)
	assert.EqualValues(t, len("unlisted and expired"), out["bytes"])
	reasons := map[string]string{}
	for _, s := range out["skipped"].([]any) {
		m := s.(map[string]any)
		reasons[m["id"].(string)] = m["reason"].(string)
	}
	assert.Contains(t, reasons[waiting], "waiting")
	assert.Contains(t, reasons[listed], "lists it")
	assert.Contains(t, reasons[uploaded], "uploaded")
	assert.Equal(t, "kept", reasons[kept])
	assert.Contains(t, reasons[plain], "not a received file")
	assert.Contains(t, reasons[strings.Repeat("0", 64)], "not a received file")
	assert.Len(t, reasons, 6, "the duplicate id in the request is one entry, and the deleted one is not skipped")

	_, err = os.Stat(rig.object(exact))
	assert.True(t, os.IsNotExist(err), "the object is gone")
	assert.Empty(t, pendingRecordPaths(rig.coralDir, exact), "and its pending receipts")
	for _, id := range []string{waiting, listed, uploaded, kept, plain} {
		_, err := os.Stat(rig.object(id))
		assert.NoError(t, err, "left alone: %s", id)
	}
	// Asking again: it is already gone.
	code, out = postUnlisted(t, rig, dash, map[string]any{"ids": []string{exact}})
	require.Equal(t, http.StatusOK, code, out)
	assert.EqualValues(t, 0, out["deleted"])
}

// The Operator's ordinary delete of a file also drops its pending receipts, so nothing is counted for a file that is gone.
func TestOperatorDeleteDropsPendingReceipts(t *testing.T) {
	rig := newArtifactRig(t)
	bh := &BoardHandler{coralDir: rig.coralDir}
	id, _, err := storeArtifact(rig.coralDir, "", "f.txt", "text/plain", []byte("a received file\n"))
	require.NoError(t, err)
	require.NoError(t, bh.writePendingReceipt("src-1", "m1", crossTeamArtifactRecord{ID: id, Name: "f.txt", Size: 16, Board: "beta", OtherBoard: "alpha", Server: "src-1", At: time.Now().UTC().Format(time.RFC3339)}))
	require.Len(t, pendingRecordPaths(rig.coralDir, id), 1)
	code, out := rig.del(id, map[string]string{testDashboardHeader: "1"})
	require.Equal(t, http.StatusOK, code, out)
	assert.Empty(t, pendingRecordPaths(rig.coralDir, id))
	used, _, _, _ := receivedUsage(rig.coralDir, time.Now())
	assert.EqualValues(t, 0, used)
}

// A file another board lists is not unlisted, whatever its pending receipt here says.
func TestReceivedFileListedOnAnotherBoardIsNotUnlisted(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	data := []byte("received twice\n")
	status, out := e.sendUnder(relaySrc, "m1", data)
	require.Equal(t, 200, status, out)
	later := e.now.Add(receivedPendingFor + time.Minute)
	_, _, _, unlisted := receivedUsage(e.dir, later)
	require.Len(t, unlisted, 1)
	require.NoError(t, e.h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: hexOf(data), Name: "f.txt", Size: int64(len(data)), Board: "gamma", OtherBoard: "alpha", Server: relaySrc}))
	used, objects, _, unlisted := receivedUsage(e.dir, later)
	assert.Empty(t, unlisted, "listed on gamma")
	assert.EqualValues(t, len(data), used, "and still counted once")
	assert.Equal(t, 1, objects)
}

// The case #4451 names: a file stored through the receive route whose message never comes, 30 minutes later: it is still counted, and a new file
// over the cap is refused.
func TestReceivedFileWithoutItsMessageStaysInTheCapAfterTheWait(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	setReceivedCap(e, 20)
	a, b := []byte("aaaaaaaaa\n"), []byte("bbbbbbbbbbb\n")
	status, out := e.sendUnder(relaySrc, "m1", a)
	require.Equal(t, 200, status, out)
	e.now = e.now.Add(receivedPendingFor + time.Minute) // its message never came
	used, objects, _, unlisted := receivedUsage(e.dir, e.now)
	assert.EqualValues(t, len(a), used, "still counted after the wait")
	assert.Equal(t, 1, objects)
	require.Len(t, unlisted, 1)
	status, out = e.sendUnder(relaySrc, "m2", b) // 10 + 12 > 20
	assert.Equal(t, 413, status)
	assert.Equal(t, "store_full", out["error"], "a new file over the cap is refused: %v", out)
	status, out = e.sendUnder(relaySrc, "m3", []byte("small\n")) // 10 + 6 <= 20
	assert.Equal(t, 200, status, "one that fits still goes in: %v", out)
}
