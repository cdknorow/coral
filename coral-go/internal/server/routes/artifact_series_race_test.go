package routes

import (
	"encoding/json"
	"os"
	"path/filepath"

	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentFirstVersionsOfOneArtifactMakeOneSeries(t *testing.T) {
	for round := 0; round < 15; round++ {
		rig, _ := ownerRig(t)
		v1 := rig.uploadText("a1", "plan.txt", "plan, first draft")
		var wg sync.WaitGroup
		series := make([]string, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				who := []string{"a1", "a2"}[i%2]
				code, out := rig.uploadVersion(who, "plan.txt", fmt.Sprintf("draft %d", i), "coral://artifacts/"+v1)
				if code == http.StatusCreated {
					series[i], _ = out["series_id"].(string)
				}
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for _, s := range series {
			if s != "" {
				seen[s] = true
			}
		}
		sid, n, in := digestSeries(rig.coralDir, v1)
		if len(seen) > 1 {
			t.Fatalf("round %d: %d different series ids were handed out for one artifact: %v (v1 now in %s v%d %v)", round, len(seen), strings.Join(seriesIDsOf(seen), ","), sid, n, in)
		}
	}
}

func seriesIDsOf(m map[string]bool) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	return o
}

// A read with a good index walks no upload records, however many the store holds (Security's SHOULD on #6406): the index is read, each row checked against its own record. The walk comes
// back when the index is missing or a row fails its record, and a version whose index row was never written is found by the next write, which heals the index.
func TestAReadWithAGoodIndexWalksNoUploadRecords(t *testing.T) {
	rig, _ := ownerRig(t)
	sid, d := seriesOf(t, rig)
	for i := 0; i < 300; i++ { // unrelated records, written straight into the store (uploads are rate limited)
		b, err := json.Marshal(agentUploadRecord{Board: "team-a"})
		require.NoError(t, err)
		require.NoError(t, writeFileAtomic(filepath.Join(uploadsDir(rig.coralDir, "a1"), fmt.Sprintf("%064x", i+1)+".json"), b, 0o600))
	}
	before := uploadRecordWalks.Load()
	ms := seriesMembers(rig.coralDir, sid)
	require.Len(t, ms, 3)
	assert.Equal(t, before, uploadRecordWalks.Load(), "a good index: no walk of the records")

	require.NoError(t, os.Remove(seriesPath(rig.coralDir, sid)))
	require.Len(t, seriesMembers(rig.coralDir, sid), 3)
	assert.Greater(t, uploadRecordWalks.Load(), before, "no index: the records are walked")

	// an upload that stopped before its index row was written: the index lacks the newest version; the next write walks, finds it and heals the index
	idx, ok := readSeriesIndex(rig.coralDir, sid)
	if !ok { // the index was removed above and a read does not write it back: make one that lacks the newest
		idx = seriesIndex{ID: sid, Board: "team-a", Versions: []seriesVersionRow{{Digest: d[0], N: 1, Session: "a1"}, {Digest: d[1], N: 2, Session: "a1"}}}
	}
	idx.Versions = idx.Versions[:2]
	require.NoError(t, writeSeriesIndex(rig.coralDir, idx))
	code, out := rig.uploadVersion("a1", "plan.txt", "plan, fourth draft", "coral://artifacts/series/"+sid)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	healed, _ := readSeriesIndex(rig.coralDir, sid)
	assert.Len(t, healed.Versions, 4, "the version the stale index lacked is in it now")
}
