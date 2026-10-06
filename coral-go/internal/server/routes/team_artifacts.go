package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
)

const (
	defaultTeamArtifactLimit = 100
	maxTeamArtifactLimit     = 200
	maxTeamArtifactOffset    = 10000
	maxArtifactMetaBytes     = 64 << 10
)

var digestLikeName = regexp.MustCompile(`^(?:sha256:)?[a-fA-F0-9]{32,64}$`)

var coralArtifactURI = regexp.MustCompile(`^(?:coral://artifacts/|/api/artifacts/)([a-fA-F0-9]{64})$`)

// teamArtifact is one deduplicated artifact reference in a team's task results.
type teamArtifact struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	MediaType      string `json:"media_type,omitempty"`
	Size           *int64 `json:"size"`
	CreatedAt      string `json:"created_at"`
	TaskID         int64  `json:"task_id"`
	TaskTitle      string `json:"task_title"`
	Source         string `json:"source"`
	URI            string `json:"uri,omitempty"`
	Digest         string `json:"digest,omitempty"`
	Available      bool   `json:"available"`
	Inline         bool   `json:"inline"`
	ReferenceCount int    `json:"reference_count"`
	PreviewURL     string `json:"preview_url,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	ContentURL     string `json:"content_url,omitempty"`
	// SessionID/ProducedBy are the stored-ownership attribution (null when it
	// cannot be established); see board.ArtifactProducer.
	SessionID  *string `json:"session_id"`
	ProducedBy *string `json:"produced_by"`

	index   int
	managed string // 64-hex Coral object id for coral-managed artifacts
}

func (h *BoardHandler) artifactObjectDir() string {
	if h.coralDir == "" {
		return ""
	}
	return filepath.Join(h.coralDir, "artifacts", "objects")
}

func parseBoundedInt(raw string, def, min, max int) (int, bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
}

// ListTeamArtifacts lists artifacts referenced by the selected team's task
// results. It reads only task workflow records and, for the returned page, the
// small metadata sidecar of Coral-managed objects (and stats the blob); it never scans the artifact
// directory or loads artifact bytes.
// GET /api/board/{project}/artifacts?limit=&offset=
func (h *BoardHandler) ListTeamArtifacts(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	limit, ok := parseBoundedInt(r.URL.Query().Get("limit"), defaultTeamArtifactLimit, 1, maxTeamArtifactLimit)
	if !ok {
		errBadRequest(w, fmt.Sprintf("limit must be an integer between 1 and %d", maxTeamArtifactLimit))
		return
	}
	offset, ok := parseBoundedInt(r.URL.Query().Get("offset"), 0, 0, maxTeamArtifactOffset)
	if !ok {
		errBadRequest(w, fmt.Sprintf("offset must be an integer between 0 and %d", maxTeamArtifactOffset))
		return
	}
	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if len(sessionFilter) > 128 || strings.ContainsAny(sessionFilter, "\x00\r\n\t") {
		errBadRequest(w, "session_id must be at most 128 characters without control characters")
		return
	}
	subscribers, err := h.bs.TeamSubscriberSessions(r.Context(), project)
	if err != nil {
		errInternalServer(w, "could not list artifacts")
		return
	}
	tasks, scanTruncated, err := h.bs.ListTeamArtifactTasksQuery(r.Context(), board.TeamArtifactQuery{
		Project: project, Limit: board.MaxTeamArtifactTaskScan, SessionID: sessionFilter, Subscribers: subscribers,
	})
	if err != nil {
		errInternalServer(w, "could not list artifacts")
		return
	}
	items := collectTeamArtifacts(project, tasks, subscribers, sessionFilter)
	end := offset + limit
	hasMore := end < len(items)
	if offset > len(items) {
		offset = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	dir := h.artifactObjectDir()
	for i := range page {
		enrichTeamArtifact(&page[i], dir)
		if digestLikeName.MatchString(page[i].Name) || strings.TrimSpace(page[i].Name) == "" {
			page[i].Name = fmt.Sprintf("Task #%d artifact", page[i].TaskID)
		}
	}
	if page == nil {
		page = []teamArtifact{}
	}
	resp := map[string]any{
		"project": project, "artifacts": page, "limit": limit, "offset": offset,
		"has_more": hasMore, "truncated": scanTruncated,
	}
	if sessionFilter != "" {
		resp["session_id"] = sessionFilter
	}
	writeJSON(w, http.StatusOK, resp)
}

// collectTeamArtifacts flattens task references into deduplicated items. When
// sessionFilter is set, artifacts not attributed to that session are dropped
// before ordering and dedupe, so repeated objects collapse per agent.
func collectTeamArtifacts(project string, tasks []board.TeamArtifactTask, subscribers map[string]string, sessionFilter string) []teamArtifact {
	var all []teamArtifact
	add := func(t board.TeamArtifactTask, source, created string, refs []board.TaskArtifact) {
		if created == "" {
			created = t.CreatedAt
		}
		producer, session := board.ArtifactProducer(t, source, subscribers)
		if sessionFilter != "" && session != sessionFilter {
			return
		}
		for i, a := range refs {
			item := teamArtifact{Name: a.Name, MediaType: a.MediaType, CreatedAt: created, TaskID: t.TaskID, TaskTitle: t.Title, Source: source, URI: a.URI, Digest: a.Digest, index: i, ReferenceCount: 1}
			if producer != "" {
				p := producer
				item.ProducedBy = &p
			}
			if session != "" {
				sid := session
				item.SessionID = &sid
			}
			switch {
			case a.URI != "":
				if m := coralArtifactURI.FindStringSubmatch(a.URI); m != nil {
					item.managed = strings.ToLower(m[1])
					item.ID = item.managed
				} else {
					item.ID = "uri:" + a.URI
				}
			case a.Content != "":
				n := int64(len(a.Content))
				item.Size, item.Inline, item.Available = &n, true, true
				item.ID = fmt.Sprintf("task-%d-%s-%d", t.TaskID, source, i)
				if item.MediaType == "" {
					item.MediaType = "text/plain; charset=utf-8"
				}
				item.ContentURL = fmt.Sprintf("/api/board/%s/tasks/%d/artifact-content?source=%s&index=%d", url.PathEscape(project), t.TaskID, source, i)
			default:
				continue
			}
			all = append(all, item)
		}
	}
	for _, t := range tasks {
		add(t, "completion", t.CompletedAt, t.Artifacts)
		if t.Review != nil {
			add(t, "review", t.Review.SubmittedAt, t.Review.Artifacts)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt > all[j].CreatedAt
		}
		if all[i].TaskID != all[j].TaskID {
			return all[i].TaskID > all[j].TaskID
		}
		if all[i].Source != all[j].Source {
			return all[i].Source < all[j].Source
		}
		return all[i].index < all[j].index
	})
	// Repeated references to one stored object or URI collapse to the newest.
	seen := make(map[string]int, len(all))
	out := all[:0:0]
	for _, it := range all {
		if at, dup := seen[it.ID]; dup {
			out[at].ReferenceCount++
			continue
		}
		seen[it.ID] = len(out)
		out = append(out, it)
	}
	return out
}

// enrichTeamArtifact fills size, availability and URLs. Only the sidecar
// metadata file of Coral-managed objects is read, never the object itself.
func enrichTeamArtifact(it *teamArtifact, objectDir string) {
	switch {
	case it.managed != "":
		it.PreviewURL = "/api/artifacts/" + it.managed
		it.DownloadURL = it.PreviewURL
		if objectDir == "" {
			return
		}
		path := filepath.Join(objectDir, it.managed)
		// Availability is the stored blob itself (stat only, never read), so
		// stale or orphaned metadata cannot report a missing object as present.
		blob, err := os.Stat(path)
		if err != nil || !blob.Mode().IsRegular() {
			return
		}
		size := blob.Size()
		it.Size, it.Available = &size, true
		metaPath := path + ".json"
		st, err := os.Stat(metaPath)
		if err != nil || st.Size() > maxArtifactMetaBytes {
			return
		}
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return
		}
		var meta agentArtifactMeta
		if json.Unmarshal(data, &meta) != nil {
			return
		}
		if meta.MediaType != "" {
			it.MediaType = meta.MediaType
		}
		if it.Digest == "" {
			it.Digest = meta.Digest
		}
		if digestLikeName.MatchString(it.Name) && meta.Name != "" && !digestLikeName.MatchString(meta.Name) {
			it.Name = meta.Name
		}
	case it.Inline:
		// Already complete; content is served by ContentURL.
	default:
		// External URI: Coral does not fetch it, so only report the link.
		it.Available = strings.HasPrefix(it.URI, "http://") || strings.HasPrefix(it.URI, "https://")
		if it.Available {
			it.PreviewURL, it.DownloadURL = it.URI, it.URI
		}
	}
}

// TeamArtifactContent serves an inline task artifact as plain text.
// GET /api/board/{project}/tasks/{taskID}/artifact-content?source=completion|review&index=N
func (h *BoardHandler) TeamArtifactContent(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid task ID")
		return
	}
	index, ok := parseBoundedInt(r.URL.Query().Get("index"), 0, 0, 1<<20)
	if !ok {
		errBadRequest(w, "invalid index")
		return
	}
	task, err := h.bs.GetTask(r.Context(), project, taskID)
	if err != nil || task == nil {
		errNotFound(w, "Task not found")
		return
	}
	var refs []board.TaskArtifact
	switch r.URL.Query().Get("source") {
	case "completion", "":
		refs = task.Workflow.Artifacts
	case "review":
		if task.Workflow.CompletionReview != nil {
			refs = task.Workflow.CompletionReview.Artifacts
		}
	default:
		errBadRequest(w, "source must be completion or review")
		return
	}
	if index >= len(refs) || refs[index].Content == "" {
		errNotFound(w, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(refs[index].Name)))
	_, _ = w.Write([]byte(refs[index].Content))
}
