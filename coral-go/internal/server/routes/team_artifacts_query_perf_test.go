package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The cost of the Team Artifacts query at 10,000 files (#4572). Opt-in: CORAL_PERF_ARTIFACTS=1 go test ./internal/server/routes
// -run TeamArtifactsQuery10k -v. Each file is an upload with a stored blob and sidecar, as a real one has.
func TestTeamArtifactsQuery10kCost(t *testing.T) {
	if os.Getenv("CORAL_PERF_ARTIFACTS") != "1" {
		t.Skip("set CORAL_PERF_ARTIFACTS=1 to measure")
	}
	srv, h, coralDir := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	dir := uploadsDir(coralDir, sessAlice)
	objDir := filepath.Join(coralDir, "artifacts", "objects")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.MkdirAll(objDir, 0o755))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10000; i++ {
		content := fmt.Sprintf("file %d %s", i, "padding")
		kind, name := "text/markdown", fmt.Sprintf("report-%05d.md", i)
		if i%5 == 0 {
			kind, name = "image/png", fmt.Sprintf("shot-%05d.png", i)
		}
		id := storeTyped(t, coralDir, content, name, kind)
		rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: name, MediaType: kind, Size: int64(len(content)), Digest: "sha256:" + id}, CreatedAt: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)})
		require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	}
	// The upload directory cache trusts a folder only once it has been unchanged for a while: let it settle, as it does in use.
	time.Sleep(4 * time.Second)
	time1 := func(label, q string) {
		for round := 0; round < 3; round++ {
			start := time.Now()
			resp, err := http.Get(srv.URL + "/api/board/alpha/artifacts" + q)
			require.NoError(t, err)
			var r struct {
				Total *int `json:"total"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
			resp.Body.Close()
			total := -1
			if r.Total != nil {
				total = *r.Total
			}
			t.Logf("%-34s run %d: %8s  total=%d", label, round+1, time.Since(start).Round(time.Millisecond), total)
		}
	}
	time1("sort=oldest (no file reads)", "?sort=oldest&limit=100")
	time1("plain list (limit 100)", "")
	time1("sort=newest (no file reads)", "?sort=newest&limit=100")
	time1("origin=upload (no file reads)", "?origin=upload&limit=100")
	time1("since/until (no file reads)", "?since=2026-01-03T00:00:00Z&until=2026-01-04T00:00:00Z")
	time1("q=report-0042 (reads sidecars)", "?q=report-0042")
	time1("kind=image (reads sidecars)", "?kind=image&limit=100")
	time1("sort=size (reads sidecars)", "?sort=size&limit=100")
	time1("sort=name (reads sidecars)", "?sort=name&limit=100")
}
