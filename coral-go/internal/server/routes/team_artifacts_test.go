package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func teamArtifactServer(t *testing.T) (*httptest.Server, *BoardHandler, string) {
	t.Helper()
	_, h := setupBoardTestServer(t)
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, nil)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/artifacts", h.ListTeamArtifacts)
	r.Get("/api/board/{project}/tasks/{taskID}/artifact-content", h.TeamArtifactContent)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, h, coralDir
}

func storeManagedArtifact(t *testing.T, coralDir, content string, withBlob, withMeta bool) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(sum[:])
	dir := filepath.Join(coralDir, "artifacts", "objects")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if withBlob {
		require.NoError(t, os.WriteFile(filepath.Join(dir, id), []byte(content), 0o644))
	}
	if withMeta {
		meta, _ := json.Marshal(agentArtifactMeta{Name: "m", MediaType: "text/markdown", Size: 999999, Digest: "sha256:" + id})
		require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), meta, 0o644))
	}
	return id
}

func completeWithArtifacts(t *testing.T, h *BoardHandler, project, title string, arts []board.TaskArtifact) int64 {
	t.Helper()
	ctx := context.Background()
	task, err := h.bs.CreateTask(ctx, project, title, "", "medium", "lead")
	require.NoError(t, err)
	_, err = h.bs.CompleteTaskWithArtifacts(ctx, project, task.ID, "lead", nil, "success", arts)
	require.NoError(t, err)
	return task.ID
}

type teamArtifactsResponse struct {
	Project   string         `json:"project"`
	Artifacts []teamArtifact `json:"artifacts"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset"`
	HasMore   bool           `json:"has_more"`
	Truncated bool           `json:"truncated"`
}

func getTeamArtifacts(t *testing.T, srv *httptest.Server, query string, wantStatus int) teamArtifactsResponse {
	t.Helper()
	resp, err := http.Get(srv.URL + query)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)
	var out teamArtifactsResponse
	if wantStatus == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return out
}

func TestTeamArtifactsIsolationOrderDedupeAndMissing(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	present := storeManagedArtifact(t, coralDir, "# report\n", true, true)
	staleMetaOnly := storeManagedArtifact(t, coralDir, "stale", false, true)
	absent := storeManagedArtifact(t, coralDir, "absent", false, false)
	other := storeManagedArtifact(t, coralDir, "other team secret", true, true)

	first := completeWithArtifacts(t, h, "alpha", "Build", []board.TaskArtifact{
		{Name: "report.md", URI: "coral://artifacts/" + present},
		{Name: "stale", URI: "coral://artifacts/" + staleMetaOnly},
		{Name: "gone", URI: "coral://artifacts/" + absent},
		{Name: "notes", Content: "inline notes"},
		{Name: "link", URI: "https://example.com/doc"},
	})
	// A later task re-references the same stored object: collapses to newest.
	second := completeWithArtifacts(t, h, "alpha", "Re-run", []board.TaskArtifact{{Name: "report-v2.md", URI: "coral://artifacts/" + present}})
	completeWithArtifacts(t, h, "beta", "Other team", []board.TaskArtifact{{Name: "secret", URI: "coral://artifacts/" + other}})
	completeWithArtifacts(t, h, "alpha", "No artifacts", nil)

	resp := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	require.Equal(t, "alpha", resp.Project)
	require.False(t, resp.HasMore)
	require.Len(t, resp.Artifacts, 5, "5 distinct artifacts: 4 from the first task plus the deduped report")
	byName := map[string]teamArtifact{}
	for _, a := range resp.Artifacts {
		byName[a.Name] = a
		require.NotEqual(t, other, a.ID, "other team's artifact must never be listed")
	}
	require.NotContains(t, byName, "secret")

	// Dedupe keeps the newest reference and counts the rest.
	report := byName["report-v2.md"]
	require.Equal(t, present, report.ID)
	require.Equal(t, second, report.TaskID)
	require.Equal(t, 2, report.ReferenceCount)
	require.True(t, report.Available)
	require.NotNil(t, report.Size)
	require.EqualValues(t, len("# report\n"), *report.Size, "size comes from the stored blob, not stale metadata")
	require.Equal(t, "/api/artifacts/"+present, report.PreviewURL)
	require.Equal(t, report.PreviewURL, report.DownloadURL)
	require.Equal(t, "text/markdown", report.MediaType)

	// Missing blobs are reported unavailable even if metadata exists.
	require.False(t, byName["stale"].Available, "stale sidecar metadata must not imply availability")
	require.Nil(t, byName["stale"].Size)
	require.False(t, byName["gone"].Available)

	inline := byName["notes"]
	require.True(t, inline.Inline)
	require.True(t, inline.Available)
	require.EqualValues(t, len("inline notes"), *inline.Size)
	require.Equal(t, first, inline.TaskID)
	require.Equal(t, fmt.Sprintf("/api/board/alpha/tasks/%d/artifact-content?source=completion&index=3", first), inline.ContentURL)

	link := byName["link"]
	require.True(t, link.Available)
	require.Equal(t, "https://example.com/doc", link.PreviewURL)
	require.Nil(t, link.Size)

	// Newest task first.
	require.Equal(t, second, resp.Artifacts[0].TaskID)

	// Another team sees only its own artifact; an unknown team is empty, not an error.
	beta := getTeamArtifacts(t, srv, "/api/board/beta/artifacts", http.StatusOK)
	require.Len(t, beta.Artifacts, 1)
	require.Equal(t, other, beta.Artifacts[0].ID)
	empty := getTeamArtifacts(t, srv, "/api/board/nobody/artifacts", http.StatusOK)
	require.Empty(t, empty.Artifacts)
}

func TestTeamArtifactsPaginationAndBounds(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	for i := 0; i < 5; i++ {
		completeWithArtifacts(t, h, "alpha", fmt.Sprintf("t%d", i), []board.TaskArtifact{{Name: fmt.Sprintf("a%d", i), Content: "x"}})
	}
	page1 := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?limit=2", http.StatusOK)
	require.Len(t, page1.Artifacts, 2)
	require.True(t, page1.HasMore)
	page3 := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?limit=2&offset=4", http.StatusOK)
	require.Len(t, page3.Artifacts, 1)
	require.False(t, page3.HasMore)
	require.Equal(t, "a0", page3.Artifacts[0].Name, "oldest last")
	require.Equal(t, "a4", page1.Artifacts[0].Name)
	past := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?offset=50", http.StatusOK)
	require.Empty(t, past.Artifacts)

	for _, q := range []string{"limit=0", "limit=201", "limit=abc", "offset=-1", "offset=10001"} {
		getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?"+q, http.StatusBadRequest)
	}
}

func TestTeamArtifactContentIsTeamScoped(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	id := completeWithArtifacts(t, h, "alpha", "Inline", []board.TaskArtifact{{Name: "x.html", Content: "<script>1</script>", MediaType: "text/html"}, {Name: "link", URI: "https://example.com"}})
	get := func(path string) (*http.Response, string) {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return resp, string(buf[:n])
	}
	resp, body := get(fmt.Sprintf("/api/board/alpha/tasks/%d/artifact-content?index=0", id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "<script>1</script>", body)
	require.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"), "active content is never served as HTML")
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))

	// The same task id requested through another team is not found.
	resp, _ = get(fmt.Sprintf("/api/board/beta/tasks/%d/artifact-content?index=0", id))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	// URI-only artifact has no inline content; out-of-range and bad source fail.
	resp, _ = get(fmt.Sprintf("/api/board/alpha/tasks/%d/artifact-content?index=1", id))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = get(fmt.Sprintf("/api/board/alpha/tasks/%d/artifact-content?index=9", id))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = get(fmt.Sprintf("/api/board/alpha/tasks/%d/artifact-content?source=bogus", id))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTeamArtifactsReadableNameWhenStoredNameIsDigest(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	id := storeManagedArtifact(t, coralDir, "named", true, false)
	meta, _ := json.Marshal(agentArtifactMeta{Name: "design.md", MediaType: "text/markdown", Size: 5})
	require.NoError(t, os.WriteFile(filepath.Join(coralDir, "artifacts", "objects", id+".json"), meta, 0o644))
	other := storeManagedArtifact(t, coralDir, "unnamed", true, false)
	taskID := completeWithArtifacts(t, h, "alpha", "Names", []board.TaskArtifact{
		{Name: id, URI: "coral://artifacts/" + id},
		{Name: "sha256:" + other, URI: "coral://artifacts/" + other},
	})
	resp := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	names := map[string]bool{}
	for _, a := range resp.Artifacts {
		names[a.Name] = true
	}
	require.True(t, names["design.md"], "sidecar upload name replaces a digest-only name")
	require.True(t, names[fmt.Sprintf("Task #%d artifact", taskID)], "digest names with no better source get a task label")
}
