package routes

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cap on received files (#4441, Security's #4438 SHOULD 2): past it new received files are refused as store_full, a file already received
// adds nothing, local uploads are never affected, and nothing is deleted.

func setReceivedCap(e *artifactEnv, n int64) {
	st := storeFor(e.dir)
	st.mu.Lock()
	st.receivedMaxBytes = func() int64 { return n }
	st.mu.Unlock()
}

func TestReceivedCapRefusesNewBytesPastTheCap(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	setReceivedCap(e, 20)
	a, b, c := []byte("aaaaaaaaa\n"), []byte("bbbbbbbbb\n"), []byte("c\n")
	status, out := e.ok(a, "a.txt")
	require.Equal(t, 200, status, out)
	status, out = e.ok(b, "b.txt")
	require.Equal(t, 200, status, "10 + 10 is exactly the cap: it fits: %v", out)

	status, out = e.ok(c, "c.txt")
	assert.Equal(t, 413, status)
	assert.Equal(t, "store_full", out["error"])
	for _, name := range e.stored() {
		assert.NotContains(t, name, strings.TrimPrefix(sha256Of(c), "sha256:"), "the refused file was not stored")
	}

	status, out = e.ok(a, "a-again.txt")
	assert.Equal(t, 200, status, "a file already received adds nothing, so it is not refused at the cap: %v", out)

	// The cap counts what was received, never what local agents uploaded, and a local upload is not refused by it.
	big := bytes.Repeat([]byte("x"), 500)
	_, _, err := storeArtifact(e.dir, "s1", "local.txt", "text/plain", big)
	require.NoError(t, err, "local uploads are not limited by the received cap")
	used, objects, _, _ := receivedUsage(e.dir, e.now)
	assert.EqualValues(t, 20, used, "the local upload is not counted")
	assert.Equal(t, 2, objects)

	// Nothing was deleted to make room, and raising the cap lets the next one in.
	setReceivedCap(e, 100)
	status, out = e.ok(c, "c.txt")
	assert.Equal(t, 200, status, "raising the cap lets the refused file in: %v", out)
}

func TestReceivedCapZeroIsNoCapAndTheDefaultIsTwoGiB(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	setReceivedCap(e, 0)
	for i, d := range []string{"one one one one\n", "two two two two\n", "three three\n"} {
		status, out := e.ok([]byte(d), "f.txt")
		require.Equal(t, 200, status, "file %d: %v", i, out)
	}
	st := storeFor(e.dir)
	st.mu.Lock()
	st.receivedMaxBytes = nil
	st.mu.Unlock()
	assert.EqualValues(t, 2<<30, st.receivedCap(), "unset is 2 GiB")
}

func TestReceivedUsageCountsEachObjectOnce(t *testing.T) {
	e := newArtifactEnv(t)
	id := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	for _, rec := range []crossTeamArtifactRecord{
		{ID: id, Name: "x", Size: 5, Board: "alpha", OtherBoard: "o", Server: "s"},
		{ID: id, Name: "x", Size: 5, Board: "beta", OtherBoard: "o", Server: "s"},
		{ID: other, Name: "y", Size: 7, Board: "beta", OtherBoard: "o", Server: "s"},
	} {
		require.NoError(t, e.h.writeCrossTeamRecord("received", rec))
	}
	// A shared row, a record that does not parse and one whose name is not its id count for nothing.
	require.NoError(t, e.h.writeCrossTeamRecord("shared", crossTeamArtifactRecord{ID: strings.Repeat("ef", 32), Size: 99, Board: "beta"}))
	dir := e.h.boardRecordDir("received", "beta")
	require.NoError(t, writeFileForTest(dir+"/"+strings.Repeat("12", 32)+".json", []byte("{not json")))
	bad, _ := json.Marshal(crossTeamArtifactRecord{ID: id, Size: 1000, Board: "beta"})
	require.NoError(t, writeFileForTest(dir+"/"+hex.EncodeToString(bytes.Repeat([]byte{0x77}, 32))+".json", bad))

	used, objects, ids, _ := receivedUsage(e.dir, e.now)
	assert.EqualValues(t, 12, used)
	assert.Equal(t, 2, objects)
	assert.True(t, ids[id] && ids[other])
}

// Settings > Storage: the total, the cap, the default, and the one-time notice at 80% of a cap the Operator has not been told about.
func TestReceivedStatusAndNotice(t *testing.T) {
	rig := newArtifactRig(t)
	ctx := context.Background()
	getReceived := func() map[string]any {
		req, err := http.NewRequest(http.MethodGet, rig.srv.URL+"/api/system/artifact-store", nil)
		require.NoError(t, err)
		req.Header.Set(testDashboardHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		r, _ := out["received"].(map[string]any)
		require.NotNil(t, r, "the store status has a received block: %v", out)
		return r
	}
	bh := &BoardHandler{coralDir: rig.coralDir}
	record := func(id string, size int64) {
		require.NoError(t, bh.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: id, Name: "f", Size: size, Board: "beta", OtherBoard: "alpha", Server: "s"}))
	}

	r := getReceived()
	assert.EqualValues(t, 2<<30, r["max_bytes"], "unset: the default cap")
	assert.EqualValues(t, 2<<30, r["default_bytes"])
	assert.EqualValues(t, 0, r["bytes"])
	assert.Equal(t, false, r["notice"])

	require.NoError(t, rig.ss.SetSetting(ctx, ReceivedArtifactsMaxBytesSetting, "100"))
	record(strings.Repeat("a1", 32), 79)
	r = getReceived()
	assert.EqualValues(t, 100, r["max_bytes"])
	assert.Equal(t, false, r["notice"], "79 of 100 is under 80%")
	record(strings.Repeat("b2", 32), 1)
	r = getReceived()
	assert.EqualValues(t, 80, r["bytes"])
	assert.Equal(t, true, r["notice"], "80 of 100: the notice is due")

	require.NoError(t, rig.ss.SetSetting(ctx, ReceivedCapNoticeAckSetting, "100"))
	assert.Equal(t, false, getReceived()["notice"], "once told about this cap, it is not shown again")

	// A different cap is a new question: told again at 80% of it.
	require.NoError(t, rig.ss.SetSetting(ctx, ReceivedArtifactsMaxBytesSetting, "90"))
	assert.Equal(t, true, getReceived()["notice"])
	// 0 is no cap: no notice, and the cap reads as 0.
	require.NoError(t, rig.ss.SetSetting(ctx, ReceivedArtifactsMaxBytesSetting, "0"))
	r = getReceived()
	assert.EqualValues(t, 0, r["max_bytes"])
	assert.Equal(t, false, r["notice"])
	// An unreadable value is the default, never "no cap".
	require.NoError(t, rig.ss.SetSetting(ctx, ReceivedArtifactsMaxBytesSetting, "lots"))
	assert.EqualValues(t, 2<<30, getReceived()["max_bytes"])
}

func writeFileForTest(path string, data []byte) error { return writeFileAtomic(path, data, 0o600) }
