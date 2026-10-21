package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/pkg/hubproxy"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

func teamArtifactServer(t *testing.T) (*httptest.Server, *BoardHandler, string) {
	t.Helper()
	_, h := setupBoardTestServer(t)
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, nil)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/artifacts", h.ListTeamArtifacts)
	r.Get("/api/session-artifacts", h.ListSessionArtifacts)
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
	require.Equal(t, "/api/artifacts/"+present+"?v=2", report.PreviewURL)
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
	require.Equal(t, hubproxy.SandboxCSP, resp.Header.Get("Content-Security-Policy"), "agent-written content is sandboxed")

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

const (
	sessAlice = "11111111-aaaa-0000-0000-000000000001"
	sessBob   = "22222222-bbbb-0000-0000-000000000002"
)

func subscribeSession(t *testing.T, h *BoardHandler, project, subscriber, sessionUUID string) {
	t.Helper()
	_, err := h.bs.Subscribe(context.Background(), project, subscriber, subscriber, "claude-"+sessionUUID, nil, "all")
	require.NoError(t, err)
}

type sessionArtifactsResponse struct {
	teamArtifactsResponse
	SessionID string `json:"session_id"`
}

func getSessionArtifacts(t *testing.T, srv *httptest.Server, query string, wantStatus int) sessionArtifactsResponse {
	t.Helper()
	resp, err := http.Get(srv.URL + query)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)
	var out sessionArtifactsResponse
	if wantStatus == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return out
}

func artifactNames(items []teamArtifact) []string {
	names := make([]string, 0, len(items))
	for _, a := range items {
		names = append(names, a.Name)
	}
	return names
}

func TestTeamArtifactsSessionFilterTwoAgentsAndCrossTeam(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	// Same subscriber/session on another team must not leak into alpha.
	subscribeSession(t, h, "beta", "Alice", sessAlice)
	completeWithArtifactsAs(t, h, "alpha", "Alice", "A work", []board.TaskArtifact{{Name: "alice-report", Content: "a"}})
	completeWithArtifactsAs(t, h, "alpha", "Bob", "B work", []board.TaskArtifact{{Name: "bob-report", Content: "b"}})
	completeWithArtifactsAs(t, h, "beta", "Alice", "Other team", []board.TaskArtifact{{Name: "beta-report", Content: "c"}})

	alice := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessAlice, http.StatusOK)
	require.Equal(t, sessAlice, alice.SessionID, "response echoes the filter for stale guards")
	require.Equal(t, []string{"alice-report"}, artifactNames(alice.Artifacts))
	require.NotNil(t, alice.Artifacts[0].SessionID)
	require.Equal(t, sessAlice, *alice.Artifacts[0].SessionID)
	require.Equal(t, "Alice", *alice.Artifacts[0].ProducedBy)

	bob := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessBob, http.StatusOK)
	require.Equal(t, []string{"bob-report"}, artifactNames(bob.Artifacts))

	// Unfiltered listing is unchanged: both agents, attribution present, no echo.
	all := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	require.ElementsMatch(t, []string{"alice-report", "bob-report"}, artifactNames(all.Artifacts))
	require.Empty(t, all.SessionID)
	for _, a := range all.Artifacts {
		require.NotNil(t, a.SessionID)
	}

	// Cross-team: beta's own filter sees only beta's artifact; alpha never sees it.
	beta := getSessionArtifacts(t, srv, "/api/board/beta/artifacts?session_id="+sessAlice, http.StatusOK)
	require.Equal(t, []string{"beta-report"}, artifactNames(beta.Artifacts))
}

func TestTeamArtifactsSessionFilterRepeatedObjectAcrossAgents(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	shared := "https://example.com/shared-design"
	completeWithArtifactsAs(t, h, "alpha", "Alice", "first", []board.TaskArtifact{{Name: "design-by-alice", URI: shared}})
	completeWithArtifactsAs(t, h, "alpha", "Bob", "second", []board.TaskArtifact{{Name: "design-by-bob", URI: shared}})

	all := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	require.Len(t, all.Artifacts, 1, "unfiltered view collapses the repeated object")
	require.Equal(t, "design-by-bob", all.Artifacts[0].Name, "to the newest reference")
	require.Equal(t, 2, all.Artifacts[0].ReferenceCount)

	// Filtering happens before dedupe, so each agent sees their own reference.
	alice := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessAlice, http.StatusOK)
	require.Equal(t, []string{"design-by-alice"}, artifactNames(alice.Artifacts))
	require.Equal(t, 1, alice.Artifacts[0].ReferenceCount, "reference_count counts only this agent's references")
	bob := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessBob, http.StatusOK)
	require.Equal(t, []string{"design-by-bob"}, artifactNames(bob.Artifacts))
}

func TestTeamArtifactsSessionFilterPagination(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	for i := 0; i < 5; i++ {
		completeWithArtifactsAs(t, h, "alpha", "Alice", fmt.Sprintf("a%d", i), []board.TaskArtifact{{Name: fmt.Sprintf("alice-%d", i), Content: "x"}})
		completeWithArtifactsAs(t, h, "alpha", "Bob", fmt.Sprintf("b%d", i), []board.TaskArtifact{{Name: fmt.Sprintf("bob-%d", i), Content: "x"}})
	}
	q := "/api/board/alpha/artifacts?session_id=" + sessAlice
	p1 := getSessionArtifacts(t, srv, q+"&limit=2", http.StatusOK)
	require.Equal(t, []string{"alice-4", "alice-3"}, artifactNames(p1.Artifacts))
	require.True(t, p1.HasMore)
	p3 := getSessionArtifacts(t, srv, q+"&limit=2&offset=4", http.StatusOK)
	require.Equal(t, []string{"alice-0"}, artifactNames(p3.Artifacts))
	require.False(t, p3.HasMore, "has_more reflects only this agent's items")
}

func TestTeamArtifactsSessionFilterAbsentIdentityAndReviews(t *testing.T) {
	srv, h, _ := teamArtifactServer(t)
	ctx := context.Background()
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	// Completed by an actor with no subscriber mapping (e.g. the operator).
	completeWithArtifactsAs(t, h, "alpha", "Operator", "operator done", []board.TaskArtifact{{Name: "operator-note", Content: "o"}})
	// A review artifact submitted by Alice.
	task, err := h.bs.CreateTask(ctx, "alpha", "reviewed", "", "medium", "lead", "Alice")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "alpha", "Alice", task.ID)
	require.NoError(t, err)
	_, err = h.bs.SubmitCompletionReview(ctx, "alpha", task.ID, "Alice", "msg", "success", "needs review", []board.TaskArtifact{{Name: "review-candidate", Content: "r"}})
	require.NoError(t, err)

	all := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	byName := map[string]teamArtifact{}
	for _, a := range all.Artifacts {
		byName[a.Name] = a
	}
	require.Nil(t, byName["operator-note"].SessionID, "unresolved producer has null session_id")
	require.NotNil(t, byName["operator-note"].ProducedBy)
	require.Equal(t, "Operator", *byName["operator-note"].ProducedBy)
	require.Equal(t, "review", byName["review-candidate"].Source)
	require.Equal(t, sessAlice, *byName["review-candidate"].SessionID, "review artifacts attribute via submitted_by")

	mine := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessAlice, http.StatusOK)
	require.Equal(t, []string{"review-candidate"}, artifactNames(mine.Artifacts), "unresolved artifacts never match a filter")

	unknown := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id=33333333-unknown", http.StatusOK)
	require.Empty(t, unknown.Artifacts)
	require.Equal(t, "33333333-unknown", unknown.SessionID)

	empty := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id=", http.StatusOK)
	require.Len(t, empty.Artifacts, 2, "empty session_id is the unfiltered listing")
	require.Empty(t, empty.SessionID)

	getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+strings.Repeat("x", 129), http.StatusBadRequest)
	getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id=bad%0Aid", http.StatusBadRequest)
}

func completeWithArtifactsAs(t *testing.T, h *BoardHandler, project, actor, title string, arts []board.TaskArtifact) int64 {
	t.Helper()
	ctx := context.Background()
	task, err := h.bs.CreateTask(ctx, project, title, "", "medium", actor)
	require.NoError(t, err)
	_, err = h.bs.CompleteTaskWithArtifacts(ctx, project, task.ID, actor, nil, "success", arts)
	require.NoError(t, err)
	return task.ID
}

// An upload that no task references yet still shows in that agent's tab, and
// only in that agent's tab.
func TestTeamArtifactsListsSessionUploads(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	id := storeManagedArtifact(t, coralDir, "# draft\n", true, true)
	dir := uploadsDir(coralDir, sessAlice)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: "draft.md", MediaType: "text/markdown", Size: 8, Digest: "sha256:" + id}, CreatedAt: "2026-10-09T10:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))

	mine := getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessAlice, http.StatusOK)
	require.Len(t, mine.Artifacts, 1)
	require.Equal(t, "draft.md", mine.Artifacts[0].Name)
	require.True(t, mine.Artifacts[0].Available)
	require.Equal(t, "/api/artifacts/"+id+"?v=2", mine.Artifacts[0].PreviewURL)

	require.Empty(t, getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?session_id=sess-2", http.StatusOK).Artifacts)
	require.Len(t, getTeamArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK).Artifacts, 1, "the team view lists its members' uploads")
	require.Empty(t, uploadsDir(coralDir, "../x"))

	// A session that isn't on this board: its uploads never list under this board's name
	// (#3601, weak site W27; the filter used to list any session's uploads).
	writeUpload(t, coralDir, sessBob, "# other team\n", "other.md", "2026-10-09T11:00:00Z")
	require.Empty(t, getTeamArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessBob, http.StatusOK).Artifacts)
}

func TestSessionArtifactsForStandaloneAgent(t *testing.T) {
	srv, _, coralDir := teamArtifactServer(t)
	id := storeManagedArtifact(t, coralDir, "# draft\n", true, true)
	dir := uploadsDir(coralDir, "solo-1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: "draft.md", MediaType: "text/markdown", Size: 8, Digest: "sha256:" + id}, CreatedAt: "2026-10-09T10:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))

	resp := getTeamArtifacts(t, srv, "/api/session-artifacts?session_id=solo-1", http.StatusOK)
	require.Len(t, resp.Artifacts, 1)
	require.Equal(t, "draft.md", resp.Artifacts[0].Name)
	require.True(t, resp.Artifacts[0].Available)
	require.Empty(t, getTeamArtifacts(t, srv, "/api/session-artifacts?session_id=solo-2", http.StatusOK).Artifacts)
	getTeamArtifacts(t, srv, "/api/session-artifacts", http.StatusBadRequest)
}

// writeUpload records sessionID's upload of a managed object, as the upload API does.
func writeUpload(t *testing.T, coralDir, sessionID, content, name, createdAt string) string {
	t.Helper()
	id := storeManagedArtifact(t, coralDir, content, true, true)
	dir := uploadsDir(coralDir, sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: name, MediaType: "text/markdown", Size: int64(len(content)), Digest: "sha256:" + id}, CreatedAt: createdAt})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	return id
}

// The team view lists what the team's agents uploaded even when no task
// references it: produced_by and session_id say who, task_id is 0 ('No task').
// An upload a task also references is one row that keeps the task link, the
// same object uploaded by two members is one row, and uploads by agents of
// another team, or by no team at all, never appear.
func TestTeamArtifactsListsUntaskedUploadsOfTeamMembers(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	const sessEve = "55555555-eeee-0000-0000-000000000005"
	subscribeSession(t, h, "beta", "Eve", sessEve)

	draft := writeUpload(t, coralDir, sessAlice, "# draft\n", "draft.md", "2026-10-09T10:00:00Z")
	notes := writeUpload(t, coralDir, sessBob, "# notes\n", "notes.md", "2026-10-09T11:00:00Z")
	attached := writeUpload(t, coralDir, sessBob, "# final\n", "final.md", "2026-10-09T09:00:00Z")
	completeWithArtifactsAs(t, h, "alpha", "Bob", "ship it", []board.TaskArtifact{{Name: "final.md", URI: "coral://artifacts/" + attached}})
	shared := writeUpload(t, coralDir, sessAlice, "# shared\n", "shared.md", "2026-10-09T08:00:00Z")
	writeUpload(t, coralDir, sessBob, "# shared\n", "shared.md", "2026-10-09T08:00:00Z")
	writeUpload(t, coralDir, sessEve, "# beta\n", "beta.md", "2026-10-09T12:00:00Z")
	writeUpload(t, coralDir, "66666666-solo-0000-0000-000000000006", "# solo\n", "solo.md", "2026-10-09T13:00:00Z")

	all := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	// final.md's row is the task's completion (today), so it is the newest.
	require.Equal(t, []string{"final.md", "notes.md", "draft.md", "shared.md"}, artifactNames(all.Artifacts), "newest first; other teams' and teamless uploads excluded")
	byID := map[string]teamArtifact{}
	for _, a := range all.Artifacts {
		byID[a.ID] = a
	}
	for id, who := range map[string]string{draft: "Alice", notes: "Bob"} {
		a := byID[id]
		require.Equal(t, "upload", a.Source, who)
		require.Zero(t, a.TaskID, "untasked")
		require.Empty(t, a.TaskTitle)
		require.NotNil(t, a.ProducedBy)
		require.Equal(t, who, *a.ProducedBy)
		require.NotNil(t, a.SessionID)
		require.True(t, a.Available)
		require.Equal(t, "text/markdown", a.MediaType)
		require.NotNil(t, a.Size)
		require.NotEmpty(t, a.CreatedAt)
		require.Equal(t, "/api/artifacts/"+id+"?v=2", a.PreviewURL)
	}
	require.Equal(t, sessAlice, *byID[draft].SessionID)
	require.Equal(t, "completion", byID[attached].Source, "an upload a task references keeps its task link")
	require.NotZero(t, byID[attached].TaskID)
	require.Equal(t, "upload", byID[shared].Source, "the same object uploaded by two members is one row")

	// Paging covers uploads too.
	p := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?limit=2&offset=2", http.StatusOK)
	require.Equal(t, []string{"draft.md", "shared.md"}, artifactNames(p.Artifacts))
	require.False(t, p.HasMore)

	beta := getSessionArtifacts(t, srv, "/api/board/beta/artifacts", http.StatusOK)
	require.Equal(t, []string{"beta.md"}, artifactNames(beta.Artifacts))

	// The session filter still shows only that agent's own uploads.
	alice := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts?session_id="+sessAlice, http.StatusOK)
	require.ElementsMatch(t, []string{"draft.md", "shared.md"}, artifactNames(alice.Artifacts))
}

// An agent replaced on the team (re-subscribed with a new session) still has
// its earlier session's uploads listed, through the session recorded on the
// task it claimed.
func TestTeamArtifactsIncludesUploadsOfAMembersEarlierSession(t *testing.T) {
	srv, h, coralDir := teamArtifactServer(t)
	ctx := context.Background()
	const sessOld, sessNew = "77777777-0001-0000-0000-000000000007", "77777777-0002-0000-0000-000000000007"
	// A claim records the claimant's session when it is a live session, as in production.
	sessDB, err := storetest.Open(t, t.TempDir()+"/sessions.db")
	require.NoError(t, err)
	t.Cleanup(func() { sessDB.Close() })
	require.NoError(t, store.NewSessionStore(sessDB).RegisterLiveSession(ctx, &store.LiveSession{SessionID: sessOld, AgentType: "claude", AgentName: "carol", WorkingDir: "/tmp"}))
	h.bs.SetSessionsDB(sessDB.DB)
	subscribeSession(t, h, "alpha", "Carol", sessOld)
	task, err := h.bs.CreateTask(ctx, "alpha", "carol task", "", "medium", "lead", "Carol")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "alpha", "Carol", task.ID)
	require.NoError(t, err)
	claimed, err := h.bs.GetTask(ctx, "alpha", task.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.SessionID, "the claim recorded Carol's session")
	_, err = h.bs.CompleteTaskWithArtifacts(ctx, "alpha", task.ID, "Carol", nil, "success", []board.TaskArtifact{{Name: "result", Content: "r"}})
	require.NoError(t, err)
	writeUpload(t, coralDir, sessOld, "# old\n", "old-session.md", "2026-10-09T10:00:00Z")
	subscribeSession(t, h, "alpha", "Carol", sessNew) // Carol now runs in a new session
	writeUpload(t, coralDir, sessNew, "# new\n", "new-session.md", "2026-10-09T11:00:00Z")

	all := getSessionArtifacts(t, srv, "/api/board/alpha/artifacts", http.StatusOK)
	require.ElementsMatch(t, []string{"result", "old-session.md", "new-session.md"}, artifactNames(all.Artifacts))
}

// Upload records are re-read only when a session's directory changes.
func TestUploadDirCacheReusesUnchangedDirectories(t *testing.T) {
	settle := uploadDirSettle
	uploadDirSettle = 0 // trust the cache at once; the test moves mtimes itself
	t.Cleanup(func() { uploadDirSettle = settle })
	coralDir := t.TempDir()
	var c uploadDirCache
	id := writeUpload(t, coralDir, sessAlice, "# a\n", "first-name.md", "2026-10-09T10:00:00Z")
	dir := uploadsDir(coralDir, sessAlice)
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, past, past))

	names := func() []string {
		var out []string
		for _, u := range c.list(coralDir, sessAlice) {
			out = append(out, u.rec.Name)
		}
		return out
	}
	require.Equal(t, []string{"first-name.md"}, names())

	// Rewriting a record in place does not touch the directory: the cache answers.
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: "renamed.md"}, CreatedAt: "2026-10-09T10:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	require.NoError(t, os.Chtimes(dir, past, past))
	require.Equal(t, []string{"first-name.md"}, names(), "an unchanged directory is not re-read")

	// A new upload changes the directory: it is re-read.
	writeUpload(t, coralDir, sessAlice, "# b\n", "second.md", "2026-10-09T11:00:00Z")
	later := past.Add(time.Minute)
	require.NoError(t, os.Chtimes(dir, later, later))
	require.ElementsMatch(t, []string{"renamed.md", "second.md"}, names())

	// A directory modified within uploadDirSettle is never trusted from the cache.
	uploadDirSettle = time.Hour
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"name":"fresh.md","created_at":"x"}`), 0o644))
	require.NoError(t, os.Chtimes(dir, later, later))
	require.ElementsMatch(t, []string{"fresh.md", "second.md"}, names())

	require.NoError(t, os.RemoveAll(dir))
	require.Empty(t, names(), "a removed directory lists nothing")
}

// BenchmarkTeamArtifactUploads: a team view over 50 agents with 20 uploads each.
// "cold" re-reads every record (a new cache per request, as without caching);
// "warm" is the steady state, one stat per session directory.
func BenchmarkTeamArtifactUploads(b *testing.B) {
	coralDir := b.TempDir()
	sessions := map[string]string{}
	for a := range 50 {
		sid := fmt.Sprintf("%08d-bench-0000-0000-000000000000", a)
		sessions[sid] = fmt.Sprintf("Agent %d", a)
		dir := uploadsDir(coralDir, sid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for u := range 20 {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", sid, u)))
			id := hex.EncodeToString(sum[:])
			rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: fmt.Sprintf("file-%d.md", u), MediaType: "text/markdown", Size: 10, Digest: "sha256:" + id}, CreatedAt: fmt.Sprintf("2026-10-09T10:%02d:00Z", u)})
			if err := os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644); err != nil {
				b.Fatal(err)
			}
		}
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(dir, past, past)
	}
	b.Run("cold", func(b *testing.B) {
		for range b.N {
			var c uploadDirCache
			if n := len(c.merge(nil, coralDir, sessions)); n != 1000 {
				b.Fatalf("got %d rows", n)
			}
		}
	})
	b.Run("warm", func(b *testing.B) {
		var c uploadDirCache
		c.merge(nil, coralDir, sessions)
		b.ResetTimer()
		for range b.N {
			if n := len(c.merge(nil, coralDir, sessions)); n != 1000 {
				b.Fatalf("got %d rows", n)
			}
		}
	})
}

// #3601 (weak site W27): an agent's token lists only its own session's uploads or those of a
// session on its board; another board's session is refused. The dashboard, the API key and
// Coral Hub (no agent token) still list any session, as they see every session already.
func TestSessionArtifactsAreTiedToTheCallingAgent(t *testing.T) {
	_, h, coralDir := teamArtifactServer(t)
	sessCarol := "33333333-cccc-0000-0000-000000000003"
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	subscribeSession(t, h, "beta", "Carol", sessCarol)
	for _, sid := range []string{sessAlice, sessBob, sessCarol} {
		writeUpload(t, coralDir, sid, "# "+sid+"\n", sid[:8]+".md", "2026-10-09T10:00:00Z")
	}
	list := func(owner *store.SessionTokenOwner, sid string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/session-artifacts?session_id="+sid, nil)
		if owner != nil {
			req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, owner))
		}
		rec := httptest.NewRecorder()
		h.ListSessionArtifacts(rec, req)
		return rec.Code
	}
	alice := &store.SessionTokenOwner{SessionID: sessAlice, SessionName: "claude-" + sessAlice}
	assert.Equal(t, http.StatusOK, list(alice, sessAlice), "its own uploads")
	assert.Equal(t, http.StatusOK, list(alice, sessBob), "a session on its board")
	assert.Equal(t, http.StatusForbidden, list(alice, sessCarol), "another board's session")
	assert.Equal(t, http.StatusForbidden, list(alice, "44444444-dddd-0000-0000-000000000004"), "a session on no board")
	stranger := &store.SessionTokenOwner{SessionID: "55555555-eeee-0000-0000-000000000005", SessionName: "claude-55555555-eeee-0000-0000-000000000005"}
	assert.Equal(t, http.StatusForbidden, list(stranger, sessAlice), "an agent on no board sees only its own")
	assert.Equal(t, http.StatusOK, list(nil, sessCarol), "the dashboard or the hub (no agent token) lists any session")
}
