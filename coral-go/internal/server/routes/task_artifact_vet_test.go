package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	vetSessionX = "11111111-aaaa-4aaa-8aaa-000000000001" // Dev on team x
	vetSessionY = "22222222-bbbb-4bbb-8bbb-000000000002" // Dev on team y
)

// vetEnv: teams x and y, one agent each, with their sessions; the artifact store in coralDir.
func vetEnv(t *testing.T) (*BoardHandler, string, *httptest.Server) {
	t.Helper()
	srv, h, coralDir := teamArtifactServer(t)
	subscribeSession(t, h, "x", "Dev", vetSessionX)
	subscribeSession(t, h, "y", "Dev", vetSessionY)
	return h, coralDir, srv
}

type vetCaller struct {
	actor   Actor
	session string // the verified session token's session id, for an agent
}

func agentOf(session string) vetCaller {
	return vetCaller{actor: Actor{Kind: ActorToken, Name: "Dev", Session: "claude-" + session}, session: session}
}

var operatorCaller = vetCaller{actor: Actor{Kind: ActorOperator, Name: "Operator"}}

// completeVia completes a new task on project through the route, as c, citing arts.
func completeVia(t *testing.T, h *BoardHandler, project string, c vetCaller, review bool, arts ...board.TaskArtifact) (map[string]any, *board.Task) {
	t.Helper()
	ctx := context.Background()
	task, err := h.bs.CreateTask(ctx, project, "work "+strconv.Itoa(len(arts)), "", "medium", "Operator")
	require.NoError(t, err)
	if review {
		_, err = h.bs.ClaimTask(ctx, project, "Dev", task.ID) // a review is of a task in progress
		require.NoError(t, err)
	}
	body, _ := json.Marshal(map[string]any{"subscriber_id": c.actor.Name, "outcome": "success", "message": "done", "reason": "please review", "artifacts": arts})
	path := "/api/board/" + project + "/tasks/" + strconv.FormatInt(task.ID, 10) + "/complete"
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", project)
	rctx.URLParams.Add("taskID", strconv.FormatInt(task.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withActor(req, c.actor)
	if c.session != "" {
		req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: c.session, SessionName: "claude-" + c.session}))
	}
	rec := httptest.NewRecorder()
	if review {
		h.SubmitCompletionReview(rec, req)
	} else {
		h.CompleteTaskByID(rec, req)
	}
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	stored, err := h.bs.GetTask(ctx, project, task.ID)
	require.NoError(t, err)
	return out, stored
}

func link(id string) board.TaskArtifact {
	return board.TaskArtifact{Name: "report.md", URI: "coral://artifacts/" + id}
}

func rowFor(t *testing.T, srv *httptest.Server, project, name string) teamArtifact {
	t.Helper()
	for _, it := range getTeamArtifacts(t, srv, "/api/board/"+project+"/artifacts", http.StatusOK).Artifacts {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("no row %q on %s", name, project)
	return teamArtifact{}
}

// L1 and L3: an agent of team x citing a file only team y uploaded gets a plain, unverified row (no
// size, type or download from the store) and the completion still succeeds, saying so; once x
// uploads the same bytes, citing it gives a managed row.
func TestVetAnotherTeamsFileIsUnverifiedUntilUploaded(t *testing.T) {
	h, coralDir, srv := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionY, "team y's report", "y.md", afterJoin())

	out, task := completeVia(t, h, "x", agentOf(vetSessionX), false, link(d))
	assert.Equal(t, float64(1), out["artifacts_unverified"])
	assert.Equal(t, []any{"report.md"}, out["artifacts_unverified_names"])
	assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[0].Provenance)
	row := rowFor(t, srv, "x", "report.md")
	assert.Equal(t, provenanceUnverified, row.Provenance)
	assert.Equal(t, "uri:coral://artifacts/"+d, row.ID)
	assert.Nil(t, row.Size, "no size from the store")
	assert.Empty(t, row.MediaType)
	assert.False(t, row.Available)
	assert.Empty(t, row.DownloadURL)
	assert.Empty(t, row.PreviewURL)

	writeUpload(t, coralDir, vetSessionX, "team y's report", "mine.md", afterJoin()) // same bytes, x's own upload
	out, task = completeVia(t, h, "x", agentOf(vetSessionX), false, board.TaskArtifact{Name: "again.md", URI: "coral://artifacts/" + d})
	assert.NotContains(t, out, "artifacts_unverified")
	assert.Equal(t, provenanceUploaded, task.Workflow.Artifacts[0].Provenance)
	again := rowFor(t, srv, "x", "again.md")
	assert.Equal(t, provenanceUploaded, again.Provenance)
	assert.Equal(t, d, again.ID)
	assert.True(t, again.Available)
}

// L4: a record written by hand into x's uploads folder doesn't count: a digest field that doesn't
// name the file, or an object whose sidecar disagrees.
func TestVetAHandWrittenUploadRecordDoesNotCount(t *testing.T) {
	h, coralDir, _ := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionY, "y's bytes", "y.md", afterJoin())
	dir := uploadsDir(coralDir, vetSessionX)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	forged, _ := json.Marshal(agentUploadRecord{agentArtifactMeta{Name: "f", Digest: "sha256:" + strings.Repeat("0", 64)}, afterJoin()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, d+".json"), forged, 0o644))
	_, task := completeVia(t, h, "x", agentOf(vetSessionX), false, link(d))
	assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[0].Provenance, "digest field names another object")

	right, _ := json.Marshal(agentUploadRecord{agentArtifactMeta{Name: "f", Digest: "sha256:" + d}, afterJoin()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, d+".json"), right, 0o644))
	sidecar := filepath.Join(coralDir, "artifacts", "objects", d+".json")
	bad, _ := json.Marshal(agentArtifactMeta{Name: "m", Digest: "sha256:" + strings.Repeat("1", 64)})
	require.NoError(t, os.WriteFile(sidecar, bad, 0o644))
	h.uploads = uploadDirCache{} // the record changed within the settle window
	_, task = completeVia(t, h, "x", agentOf(vetSessionX), false, link(d))
	assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[0].Provenance, "the sidecar disagrees")
}

// L5, L6, L7: the link forms normalise the same; 63 or 65 hex digits is not a managed link; a
// missing object is unverified; a provenance the client sends is never kept.
func TestVetLinkFormsMissingObjectsAndClientProvenance(t *testing.T) {
	h, coralDir, _ := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionX, "x's bytes", "x.md", afterJoin())
	forms := []board.TaskArtifact{
		{Name: "upper", URI: "CORAL://ARTIFACTS/" + strings.ToUpper(d)},
		{Name: "space", URI: "coral://artifacts/" + d + " "},
		{Name: "short", URI: "coral://artifacts/" + d[:63]},
		{Name: "long", URI: "coral://artifacts/" + d + "0"},
		{Name: "missing", URI: "coral://artifacts/" + strings.Repeat("ab", 32)},
		{Name: "claims", URI: "https://example.com/x", Provenance: provenanceUploaded},
		{Name: "claims managed", URI: "coral://artifacts/" + strings.Repeat("cd", 32), Provenance: provenanceUploaded},
	}
	_, task := completeVia(t, h, "x", agentOf(vetSessionX), false, forms...)
	got := map[string]string{}
	for _, a := range task.Workflow.Artifacts {
		got[a.Name] = a.Provenance
	}
	assert.Equal(t, map[string]string{
		"upper": provenanceUploaded, "space": provenanceUploaded,
		"short": "", "long": "", "missing": provenanceUnverified,
		"claims": "", "claims managed": provenanceUnverified,
	}, got)
}

// L5, the form board validation refuses before anything is stored (a leading "/" reads as a local
// path): the link check itself normalises it like the others.
func TestManagedArtifactIDForms(t *testing.T) {
	d := strings.Repeat("0a", 32)
	for _, uri := range []string{"coral://artifacts/" + d, "CORAL://artifacts/" + strings.ToUpper(d), "/api/artifacts/" + d + "?v=2", " coral://artifacts/" + d + " "} {
		assert.Equal(t, d, managedArtifactID(uri), uri)
	}
	for _, uri := range []string{"coral://artifacts/" + d[:63], "coral://artifacts/" + d + "0", "coral://artifacts/" + d + "?v=x", "https://example.com/" + d} {
		assert.Empty(t, managedArtifactID(uri), uri)
	}
}

// L8: the Operator (and a Coral Hub user) citing a stored object: person, managed; a missing one:
// unverified.
func TestVetAPersonNeedsOnlyTheObject(t *testing.T) {
	h, coralDir, srv := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionY, "y's bytes", "y.md", afterJoin())
	missing := strings.Repeat("ef", 32)
	for _, c := range []vetCaller{operatorCaller, {actor: Actor{Kind: ActorHub, Name: "Operator", HubUser: "hub-user-0000abcd"}}} {
		_, task := completeVia(t, h, "x", c, false, link(d), board.TaskArtifact{Name: "gone.md", URI: "coral://artifacts/" + missing})
		assert.Equal(t, provenancePerson, task.Workflow.Artifacts[0].Provenance, c.actor.Kind)
		assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[1].Provenance, c.actor.Kind)
	}
	assert.Equal(t, d, rowFor(t, srv, "x", "report.md").ID, "managed")
}

// L9: a result recorded before the check (no provenance) lists as it always did.
func TestVetLegacyRowsListAsBefore(t *testing.T) {
	h, coralDir, srv := vetEnv(t)
	d := storeManagedArtifact(t, coralDir, "old bytes", true, true)
	completeWithArtifacts(t, h, "x", "old task", []board.TaskArtifact{link(d)})
	row := rowFor(t, srv, "x", "report.md")
	assert.Equal(t, d, row.ID)
	assert.Empty(t, row.Provenance, "a legacy row has none")
	assert.True(t, row.Available)
}

// L11: review artifacts go through the same check: a reviewer can't launder another team's file.
func TestVetReviewArtifactsToo(t *testing.T) {
	h, coralDir, _ := vetEnv(t)
	d := writeUpload(t, coralDir, vetSessionY, "y's bytes", "y.md", afterJoin())
	out, task := completeVia(t, h, "x", agentOf(vetSessionX), true, link(d))
	assert.Equal(t, float64(1), out["artifacts_unverified"])
	require.NotNil(t, task.Workflow.CompletionReview)
	assert.Equal(t, provenanceUnverified, task.Workflow.CompletionReview.Artifacts[0].Provenance)
}

// Security's INFO 3: the vet uses --attach's team set, time bound included: an upload a session
// made before it joined this board is not the team's.
func TestVetUsesTheTeamSetWithItsTimeBound(t *testing.T) {
	h, coralDir, _ := vetEnv(t)
	before := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	early := writeUpload(t, coralDir, vetSessionX, "made before joining x", "early.md", before)
	_, task := completeVia(t, h, "x", agentOf(vetSessionX), false, link(early))
	assert.Equal(t, provenanceUnverified, task.Workflow.Artifacts[0].Provenance)
}
