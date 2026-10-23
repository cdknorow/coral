package routes

// The machine-wide artifact list (#5575): GET /api/artifacts, every artifact in this computer's store with who made it, for the owner's Artifacts tab in Coral Hub (and the Operator's own
// tools). Owner only: through the hub the machine's owner (hubOwnerRequest), here the dashboard or the signed command line; an agent's token, a script and any other hub caller get 403.
// One row per artifact (its digest), newest first, a cursor to page. The uploader is the first record of the artifact; the rest is counted.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/safefs"
)

const (
	defaultAllArtifactsLimit = 100
	maxAllArtifactsLimit     = 200
	unknownWho               = "unknown"
	maxAllArtifactsCursor    = 200
)

// allArtifact is one row of GET /api/artifacts.
type allArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
	// Board is the board's id the uploading agent was on ("" when unknown); BoardLabel its name, or "unknown".
	Board      string `json:"board"`
	BoardLabel string `json:"board_label"`
	// Uploader and Role are the agent's name and role at upload ("unknown" and "" for a record made before they were kept, or a file no agent here uploaded); SessionID is its session.
	Uploader  string `json:"uploader"`
	Role      string `json:"role"`
	SessionID string `json:"session_id,omitempty"`
	// Human is who put it there when it was a person: "owner" for an image pasted or attached in this computer's dashboard; "" for an agent's own upload.
	Human string `json:"human"`
	// Source is "upload" (an agent's), "paste" (an image from the dashboard), "received" (a file that came with a cross-team message) or "unknown".
	Source string `json:"source"`
	// Records counts the sessions that uploaded the same bytes; Available is false when the file itself is gone and only its record is left.
	Records   int  `json:"records"`
	Available bool `json:"available"`
	// SeriesID, Version, Versions and IsLatest are present only on a version of an artifact series (#6357), from the upload records: the series' id, this version's number, how many versions
	// the series has and whether this is the newest. Absent on an unversioned artifact; nothing else in the row depends on them.
	SeriesID string `json:"series_id,omitempty"`
	Version  int    `json:"version,omitempty"`
	Versions int    `json:"versions,omitempty"`
	IsLatest *bool  `json:"is_latest,omitempty"`
}

type allArtifactSource struct {
	session string
	rec     agentUploadRecord
}

// listAllArtifactRows reads the store once: the objects, the sessions' upload records and the boards' received rows, joined by digest.
func listAllArtifactRows(coralDir string) []allArtifact {
	objects := filepath.Join(coralDir, "artifacts", "objects")
	entries, err := os.ReadDir(objects)
	if err != nil {
		return nil
	}
	records := map[string][]allArtifactSource{}
	if sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads")); err == nil {
		for _, s := range sessions {
			up := uploadsDir(coralDir, s.Name())
			if up == "" || !s.IsDir() {
				continue
			}
			files, err := os.ReadDir(up)
			if err != nil {
				continue
			}
			for _, f := range files {
				name := f.Name()
				if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
					continue
				}
				id := strings.TrimSuffix(name, ".json")
				if !objectNameRe.MatchString(id) {
					continue
				}
				if rec, ok := readUploadRecord(coralDir, s.Name(), id); ok {
					records[id] = append(records[id], allArtifactSource{session: s.Name(), rec: rec})
				}
			}
		}
	}
	received := map[string]crossTeamArtifactRecord{}
	if boards, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "received")); err == nil {
		for _, b := range boards {
			if !b.IsDir() || !boardDirName.MatchString(b.Name()) {
				continue
			}
			files, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "received", b.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				id := strings.TrimSuffix(f.Name(), ".json")
				if !objectNameRe.MatchString(id) || f.Name() != id+".json" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(coralDir, "artifacts", "received", b.Name(), f.Name()))
				if err != nil {
					continue
				}
				var rec crossTeamArtifactRecord
				if json.Unmarshal(data, &rec) == nil {
					if old, ok := received[id]; !ok || rec.At < old.At {
						received[id] = rec
					}
				}
			}
		}
	}
	// Series (#6357): each series' versions from the records, so the row of a version can say its number, how many there are and whether it is the newest.
	type seriesFacts struct{ count, newest int }
	facts := map[string]*seriesFacts{}
	for _, recs := range records {
		seen := map[string]bool{}
		for _, s := range recs {
			if seriesIDRe.MatchString(s.rec.SeriesID) && !seen[s.rec.SeriesID] {
				seen[s.rec.SeriesID] = true
				f := facts[s.rec.SeriesID]
				if f == nil {
					f = &seriesFacts{}
					facts[s.rec.SeriesID] = f
				}
				f.count++
				f.newest = max(f.newest, s.rec.Version)
			}
		}
	}
	var rows []allArtifact
	for _, e := range entries {
		name := e.Name()
		if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !objectNameRe.MatchString(id) {
			continue
		}
		var meta agentArtifactMeta
		if data, err := os.ReadFile(filepath.Join(objects, name)); err != nil || len(data) > maxArtifactMetaBytes || json.Unmarshal(data, &meta) != nil {
			continue
		}
		row := allArtifact{ID: id, Name: meta.Name, MediaType: meta.MediaType, Size: meta.Size, Uploader: unknownWho, BoardLabel: unknownWho, Source: unknownWho}
		if blob, err := os.Lstat(filepath.Join(objects, id)); err == nil && blob.Mode().IsRegular() {
			row.Available, row.Size = true, blob.Size()
			row.CreatedAt = blob.ModTime().UTC().Format(time.RFC3339)
		}
		if recs := records[id]; len(recs) > 0 {
			sort.Slice(recs, func(i, j int) bool {
				if recs[i].rec.CreatedAt != recs[j].rec.CreatedAt {
					return recs[i].rec.CreatedAt < recs[j].rec.CreatedAt
				}
				return recs[i].session < recs[j].session
			})
			first := recs[0]
			row.Records = len(recs)
			row.SessionID = first.session
			row.Human = first.rec.UploadedBy
			if first.rec.CreatedAt != "" {
				row.CreatedAt = first.rec.CreatedAt
			}
			row.Source = "upload"
			if first.rec.Source == sourcePaste {
				row.Source = "paste"
			}
			if first.rec.Uploader != "" {
				row.Uploader = first.rec.Uploader
			}
			row.Role = first.rec.Role
			row.Board = first.rec.Board
			for _, s := range recs {
				if f := facts[s.rec.SeriesID]; f != nil && seriesIDRe.MatchString(s.rec.SeriesID) {
					newest := s.rec.Version == f.newest
					row.SeriesID, row.Version, row.Versions, row.IsLatest = s.rec.SeriesID, s.rec.Version, f.count, &newest
					break
				}
			}
		} else if rec, ok := received[id]; ok {
			row.Source = "received"
			if rec.At != "" {
				row.CreatedAt = rec.At
			}
			row.Board = rec.Board
			if rec.By != "" {
				row.Uploader = rec.By
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt > rows[j].CreatedAt
		}
		return rows[i].ID > rows[j].ID
	})
	return rows
}

// allArtifactsCursor is the opaque position after a row: its time and id.
func allArtifactsCursor(row allArtifact) string {
	return base64.RawURLEncoding.EncodeToString([]byte(row.CreatedAt + "|" + row.ID))
}

// parseAllArtifactsCursor reads a cursor back; false for anything that is not one.
func parseAllArtifactsCursor(c string) (at, id string, ok bool) {
	if len(c) > maxAllArtifactsCursor {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", false
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found || !objectNameRe.MatchString(id) || len(at) > 40 {
		return "", "", false
	}
	return at, id, true
}

// listAllArtifacts: GET /api/artifacts?limit=&cursor=&board=
func (h *SessionsHandler) listAllArtifacts(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		if refuseNonOwner(w, r) {
			return
		}
	} else if refuseNonOperator(w, r, "list every artifact") {
		return
	}
	q := r.URL.Query()
	for k := range q {
		if k != "limit" && k != "cursor" && k != "board" {
			errBadRequest(w, "unknown parameter "+k)
			return
		}
	}
	limit, ok := parseBoundedInt(q.Get("limit"), defaultAllArtifactsLimit, 1, maxAllArtifactsLimit)
	if !ok {
		errBadRequest(w, "limit must be an integer between 1 and 200")
		return
	}
	boardFilter := q.Get("board")
	if boardFilter != "" && !safefs.IsSafeID(boardFilter) {
		errBadRequest(w, "board is not a board id")
		return
	}
	var afterAt, afterID string
	hasCursor := false
	if c := q.Get("cursor"); c != "" {
		if afterAt, afterID, ok = parseAllArtifactsCursor(c); !ok {
			errBadRequest(w, "cursor is not one this list gave")
			return
		}
		hasCursor = true
	}
	rows := listAllArtifactRows(h.cfg.CoralDir())
	if boardFilter != "" {
		kept := rows[:0]
		for _, row := range rows {
			if row.Board == boardFilter {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	total := len(rows)
	start := 0
	if hasCursor { // the rows are newest first: the page starts after the row the cursor names
		start = sort.Search(len(rows), func(i int) bool {
			if rows[i].CreatedAt != afterAt {
				return rows[i].CreatedAt < afterAt
			}
			return rows[i].ID < afterID
		})
	}
	end := start + limit
	next := ""
	if end < len(rows) {
		next = allArtifactsCursor(rows[end-1])
	} else {
		end = len(rows)
	}
	page := rows[start:end]
	h.labelAllArtifactBoards(r.Context(), page)
	if page == nil {
		page = []allArtifact{}
	}
	out := map[string]any{"artifacts": page, "total": total, "limit": limit}
	if next != "" {
		out["next_cursor"] = next
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// labelAllArtifactBoards fills BoardLabel from the board names, for the boards the page names.
func (h *SessionsHandler) labelAllArtifactBoards(ctx context.Context, page []allArtifact) {
	if h.bs == nil {
		return
	}
	var ids []string
	for _, row := range page {
		if row.Board != "" {
			ids = append(ids, row.Board)
		}
	}
	if len(ids) == 0 {
		return
	}
	labels := h.bs.BoardLabels(ctx, ids)
	for i := range page {
		if page[i].Board != "" {
			if l := labels[page[i].Board]; l != "" {
				page[i].BoardLabel = l
			} else {
				page[i].BoardLabel = page[i].Board
			}
		}
	}
}
