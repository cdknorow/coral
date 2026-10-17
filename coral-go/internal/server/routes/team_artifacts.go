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
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/go-chi/chi/v5"
)

const (
	defaultTeamArtifactLimit = 100
	maxTeamArtifactLimit     = 200
	maxTeamArtifactOffset    = 10000
	maxArtifactMetaBytes     = 64 << 10
)

var digestLikeName = regexp.MustCompile(`^(?:sha256:)?[a-fA-F0-9]{32,64}$`)

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
	// Unverified: a coral://artifacts link the team never uploaded (#4379): listed as a plain
	// link, with no size, type, preview or download from the store ("link not verified").
	Unverified bool `json:"unverified,omitempty"`
	// OtherBoard/OtherServer: for a "shared" row the board (and Coral, "local" for this one) the
	// artifact was sent to; for a "received" row the one it came from, and SentBy who sent it
	// (#4379). Received files are not scanned.
	OtherBoard  string `json:"other_board,omitempty"`
	OtherServer string `json:"other_server,omitempty"`
	SentBy      string `json:"sent_by,omitempty"`

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
	if sessionFilter != "" {
		// Only a session of this board: the filter used to list ANY session's uploads under
		// this board's name (#3601, weak site W27).
		if _, onBoard := teamUploadSessions(tasks, subscribers)[sessionFilter]; onBoard {
			items = h.uploads.merge(items, h.coralDir, map[string]string{sessionFilter: ""})
		}
	} else {
		items = h.uploads.merge(items, h.coralDir, teamUploadSessions(tasks, subscribers))
		items = h.mergeCrossTeamRows(items, project)
	}
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
			if page[i].TaskID == 0 {
				page[i].Name = "Uploaded artifact"
			} else {
				page[i].Name = fmt.Sprintf("Task #%d artifact", page[i].TaskID)
			}
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
				// A managed link counts as the team's file unless the check at completion marked it
				// unverified (#4379); legacy rows (no provenance) show as they always did.
				if id := managedArtifactID(a.URI); id != "" && a.Provenance != provenanceUnverified {
					item.managed = id
					item.ID = item.managed
				} else {
					item.ID = "uri:" + a.URI
					item.Unverified = id != ""
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
		it.PreviewURL = artifactURL(it.managed)
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
	// Already plain text; sandboxed as well, like every agent-written response.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", hubproxy.SandboxCSP)
	w.Header().Set("Content-Disposition", hubproxy.Disposition("inline", refs[index].Name))
	_, _ = w.Write([]byte(refs[index].Content))
}

// mayListSessionUploads ties a session's upload listing to the caller (#3601, weak site W27): an
// agent's token lists only its own session's uploads or those of a session on its board. The
// dashboard, the API key and Coral Hub see every session already and may list any.
func (h *BoardHandler) mayListSessionUploads(r *http.Request, sessionID string) bool {
	owner, ok := VerifiedSession(r.Context())
	if !ok || owner == nil {
		return true
	}
	if owner.SessionID != "" && owner.SessionID == sessionID {
		return true
	}
	sub, err := h.bs.GetSubscriptionBySessionName(r.Context(), owner.SessionName)
	if err != nil || sub == nil || sub.Project == "" {
		return false
	}
	sessions, err := h.bs.TeamSubscriberSessions(r.Context(), sub.Project)
	if err != nil {
		return false
	}
	for _, sid := range sessions {
		if sid == sessionID {
			return true
		}
	}
	return false
}

// teamUploadSessions maps each Coral session that belongs to the team to the
// subscriber it belongs to: every subscriber's stored session, and the session
// recorded when a team task was claimed (an agent that has since left or been
// replaced). Only these sessions' uploads appear in the team view.
func teamUploadSessions(tasks []board.TeamArtifactTask, subscribers map[string]string) map[string]string {
	out := make(map[string]string, len(subscribers))
	for _, t := range tasks {
		if t.SessionID != "" && t.AssignedTo != "" {
			out[t.SessionID] = t.AssignedTo
		}
	}
	for sub, sid := range subscribers {
		if sid != "" {
			out[sid] = sub // a subscriber's own record wins over a past claim
		}
	}
	return out
}

// sessionUpload is one parsed per-session upload record.
type sessionUpload struct {
	id  string
	rec agentUploadRecord
}

// uploadDirCache keeps each session's parsed upload records, keyed by the
// directory's modification time, so a team view over many agents stats each
// directory instead of re-reading every record on every request. A directory
// modified in the last uploadDirSettle is always re-read, so a coarse mtime
// cannot hide an upload made within the same tick. The zero value is ready.
type uploadDirCache struct {
	mu   sync.Mutex
	dirs map[string]uploadDirEntry
}

type uploadDirEntry struct {
	mod     time.Time
	uploads []sessionUpload
}

var uploadDirSettle = 2 * time.Second

// list returns the session's upload records (nil when it has none).
func (c *uploadDirCache) list(coralDir, sessionID string) []sessionUpload {
	dir := uploadsDir(coralDir, sessionID)
	if dir == "" {
		return nil
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		c.mu.Lock()
		delete(c.dirs, dir)
		c.mu.Unlock()
		return nil
	}
	mod := st.ModTime()
	settled := time.Since(mod) >= uploadDirSettle
	c.mu.Lock()
	if e, ok := c.dirs[dir]; ok && settled && e.mod.Equal(mod) {
		c.mu.Unlock()
		return e.uploads
	}
	c.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var uploads []sessionUpload
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || id == e.Name() || len(id) != 64 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || len(data) > maxArtifactMetaBytes {
			continue
		}
		var rec agentUploadRecord
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		uploads = append(uploads, sessionUpload{id: strings.ToLower(id), rec: rec})
	}
	if settled {
		c.mu.Lock()
		if c.dirs == nil {
			c.dirs = make(map[string]uploadDirEntry)
		}
		c.dirs[dir] = uploadDirEntry{mod: mod, uploads: uploads}
		c.mu.Unlock()
	}
	return uploads
}

// merge adds the listed sessions' uploaded objects that no task result in items
// references, so an upload shows right away, before (or without) any task.
// sessions maps a session id to the subscriber shown as its producer ("" for
// none). Items already attached to a task keep their task attribution; the
// same object uploaded by two sessions is one row.
func (c *uploadDirCache) merge(items []teamArtifact, coralDir string, sessions map[string]string) []teamArtifact {
	if len(sessions) == 0 {
		return items
	}
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.ID] = true
	}
	added := false
	for sessionID, producer := range sessions {
		for _, u := range c.list(coralDir, sessionID) {
			if have[u.id] {
				continue
			}
			have[u.id] = true
			sid := sessionID
			item := teamArtifact{
				ID: u.id, Name: u.rec.Name, MediaType: u.rec.MediaType, CreatedAt: u.rec.CreatedAt,
				Source: "upload", URI: "coral://artifacts/" + u.id, Digest: u.rec.Digest,
				ReferenceCount: 1, SessionID: &sid, managed: u.id,
			}
			if producer != "" {
				p := producer
				item.ProducedBy = &p
			}
			items = append(items, item)
			added = true
		}
	}
	if added {
		// Newest first; ties keep a stable order (by id) whatever the map order.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].CreatedAt != items[j].CreatedAt {
				return items[i].CreatedAt > items[j].CreatedAt
			}
			return items[i].TaskID == 0 && items[j].TaskID == 0 && items[i].ID < items[j].ID
		})
	}
	return items
}

// ListSessionArtifacts lists the files a standalone agent (one with no team
// board) uploaded. Team agents use ListTeamArtifacts, which also covers task
// results.
// GET /api/session-artifacts?session_id=&limit=&offset=
func (h *BoardHandler) ListSessionArtifacts(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, "\x00\r\n\t") {
		errBadRequest(w, "session_id is required")
		return
	}
	limit, ok := parseBoundedInt(r.URL.Query().Get("limit"), defaultTeamArtifactLimit, 1, maxTeamArtifactLimit)
	offset, ok2 := parseBoundedInt(r.URL.Query().Get("offset"), 0, 0, maxTeamArtifactOffset)
	if !ok || !ok2 {
		errBadRequest(w, "invalid limit or offset")
		return
	}
	if !h.mayListSessionUploads(r, sessionID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "an agent can list only its own uploads or those of its board's agents", "code": "forbidden_for_agent"})
		return
	}
	items := h.uploads.merge(nil, h.coralDir, map[string]string{sessionID: ""})
	end := offset + limit
	hasMore := end < len(items)
	if offset > len(items) {
		offset = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	for i := range page {
		enrichTeamArtifact(&page[i], h.artifactObjectDir())
	}
	if page == nil {
		page = []teamArtifact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifacts": page, "limit": limit, "offset": offset, "has_more": hasMore,
		"truncated": false, "session_id": sessionID,
	})
}
