package routes

// Images pasted or attached in the dashboard are artifacts too (#5284). What follows is everything about them that is not the upload itself: who may see one through Coral Hub (the
// owner's switch), removing one session's shared copy, and the retention that removes them with the upload files.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/safefs"
)

// readUploadRecord reads the upload record of artifact id in one session's folder.
func readUploadRecord(coralDir, sessionID, id string) (agentUploadRecord, bool) {
	up := uploadsDir(coralDir, sessionID)
	if up == "" || !objectNameRe.MatchString(id) {
		return agentUploadRecord{}, false
	}
	data, err := os.ReadFile(filepath.Join(up, id+".json"))
	if err != nil {
		return agentUploadRecord{}, false
	}
	var rec agentUploadRecord
	if json.Unmarshal(data, &rec) != nil {
		return agentUploadRecord{}, false
	}
	return rec, true
}

// uploadRecordCounts counts the session upload records of artifact id: all of them, and those that are pasted images. exceptSession is not counted (the session whose copy is being removed).
func uploadRecordCounts(coralDir, id, exceptSession string) (all, pasted int) {
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return 0, 0
	}
	for _, s := range sessions {
		if !s.IsDir() || s.Name() == exceptSession {
			continue
		}
		if rec, ok := readUploadRecord(coralDir, s.Name(), id); ok {
			all++
			if rec.Source == sourcePaste {
				pasted++
			}
		}
	}
	return all, pasted
}

// pasteOnlyArtifact reports whether artifact id is known only as a pasted image: it has upload records and every one of them is a paste. An artifact with a record of an agent's own upload (or no
// record at all: a received file, an older one) is not.
func pasteOnlyArtifact(coralDir, id string) bool {
	all, pasted := uploadRecordCounts(coralDir, id, "")
	return all > 0 && all == pasted
}

// hubMayNotSeePastedImage is the check the artifact route makes (#5284): a request that came through the Coral Hub tunnel, for an artifact that is only a pasted image, while the owner's
// switch "share pasted images" is off (or cannot be read: closed), is answered 404 as if there were no such artifact.
func (h *SessionsHandler) hubMayNotSeePastedImage(r *http.Request, id string) bool {
	if !cloud.Tunneled(r) {
		return false
	}
	share := false
	if h.ss != nil {
		if s, err := h.ss.GetSettings(r.Context()); err == nil {
			share = HubSharePastedImages(s)
		}
	}
	return !share && pasteOnlyArtifact(h.cfg.CoralDir(), id)
}

// otherReferences counts what else lists artifact id once one session's record is gone: the other sessions' records, pending receipts and the boards' shared and received rows.
func otherReferences(coralDir, id, exceptSession string) int {
	n, _ := uploadRecordCounts(coralDir, id, exceptSession)
	n += len(pendingRecordPaths(coralDir, id))
	for _, kind := range crossTeamRecordKinds {
		boards, err := os.ReadDir(filepath.Join(coralDir, "artifacts", kind))
		if err != nil {
			continue
		}
		for _, b := range boards {
			if b.IsDir() && boardDirName.MatchString(b.Name()) {
				if _, err := os.Stat(filepath.Join(coralDir, "artifacts", kind, b.Name(), id+".json")); err == nil {
					n++
				}
			}
		}
	}
	return n
}

// removePastedCopy removes one session's paste record of artifact id, and the artifact itself when nothing else lists it and the Operator has not kept it. It returns whether a record was
// removed and whether the artifact went with it. Two agents that were given the same bytes share one object: removing one's record never takes the other's.
func (h *SessionsHandler) removePastedCopy(ctx context.Context, sessionID, id string) (removed, blobGone bool) {
	coralDir := h.cfg.CoralDir()
	defer lockArtifact(id)()
	rec, ok := readUploadRecord(coralDir, sessionID, id)
	if !ok || rec.Source != sourcePaste {
		return false, false
	}
	if os.Remove(filepath.Join(uploadsDir(coralDir, sessionID), id+".json")) != nil {
		return false, false
	}
	if otherReferences(coralDir, id, sessionID) > 0 {
		return true, false
	}
	if h.db != nil {
		if kept, err := h.isKept(ctx, id); err != nil || kept {
			return true, false
		}
	}
	if _, _, found, err := deleteArtifactFilesLocked(coralDir, id); err != nil || !found {
		return true, false
	}
	h.dropArtifactComments(ctx, id)
	return true, true
}

// deleteSharedImage: DELETE /api/shared-images/{id}?session_id= removes this agent's shared copy of an image pasted in the dashboard (#5284). The Operator only (this computer's dashboard or the
// signed command line), never through the tunnel. The agent's own file is not touched; the link in the chat then shows the file's name.
func (h *SessionsHandler) deleteSharedImage(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) || !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can remove a shared image, from this computer's dashboard or the signed command line")
		return
	}
	id := chi.URLParam(r, "id")
	sid := r.URL.Query().Get("session_id")
	if !objectNameRe.MatchString(id) || !safefs.IsSafeID(sid) {
		errBadRequest(w, "an artifact id and a session_id are required")
		return
	}
	removed, gone := h.removePastedCopy(r.Context(), sid, id)
	if !removed {
		errNotFound(w, "no pasted image of that agent with that id")
		return
	}
	slog.Info("[artifacts] a pasted image's shared copy was removed by the Operator", "digest", id, "artifact_removed", gone)
	writeJSON(w, http.StatusOK, map[string]any{"removed": true, "artifact_removed": gone})
}

// PurgePastedImages removes the paste records older than cutoff (the uploads retention, #5284) and each artifact nothing else lists. It returns the records and the artifacts removed.
func (h *SessionsHandler) PurgePastedImages(ctx context.Context, cutoff time.Time) (records, blobs int) {
	coralDir := h.cfg.CoralDir()
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return 0, 0
	}
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		up := uploadsDir(coralDir, s.Name())
		if up == "" {
			continue
		}
		files, err := os.ReadDir(up)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if len(name) != 69 || name[64:] != ".json" || !objectNameRe.MatchString(name[:64]) {
				continue
			}
			rec, ok := readUploadRecord(coralDir, s.Name(), name[:64])
			if !ok || rec.Source != sourcePaste {
				continue
			}
			when, perr := time.Parse(time.RFC3339, rec.CreatedAt)
			if perr != nil || when.After(cutoff) {
				continue
			}
			if removed, gone := h.removePastedCopy(ctx, s.Name(), name[:64]); removed {
				records++
				if gone {
					blobs++
				}
			}
		}
	}
	if records > 0 {
		slog.Info("[artifacts] pasted images removed with the uploads retention", "records", records, "artifacts", blobs)
	}
	return records, blobs
}

// pasteOnlyIDs is the set of artifact ids that are known only as pasted images (every upload record of theirs is a paste), from one pass over the upload records.
func pasteOnlyIDs(coralDir string) map[string]bool {
	counts := map[string][2]int{}
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return nil
	}
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		up := uploadsDir(coralDir, s.Name())
		if up == "" {
			continue
		}
		files, err := os.ReadDir(up)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if len(name) != 69 || name[64:] != ".json" || !objectNameRe.MatchString(name[:64]) {
				continue
			}
			rec, ok := readUploadRecord(coralDir, s.Name(), name[:64])
			if !ok {
				continue
			}
			c := counts[name[:64]]
			c[0]++
			if rec.Source == sourcePaste {
				c[1]++
			}
			counts[name[:64]] = c
		}
	}
	out := map[string]bool{}
	for id, c := range counts {
		if c[0] > 0 && c[0] == c[1] {
			out[id] = true
		}
	}
	return out
}

// hidePastedFromHub removes the pasted-only images from an artifact list when the request came through the Coral Hub tunnel and the owner's switch "share pasted images" is off (or cannot be
// read: closed). The artifact route answers 404 for their bytes (hubMayNotSeePastedImage); the lists must not show their names, ids and sizes either (#5284, Security's review).
func hidePastedFromHub(r *http.Request, coralDir string, items []teamArtifact) []teamArtifact {
	if !cloud.Tunneled(r) || len(items) == 0 {
		return items
	}
	share := false
	if uploadSettings != nil {
		if s, err := uploadSettings(r.Context()); err == nil {
			share = HubSharePastedImages(s)
		}
	}
	if share {
		return items
	}
	hidden := pasteOnlyIDs(coralDir)
	if len(hidden) == 0 {
		return items
	}
	out := make([]teamArtifact, 0, len(items))
	for _, it := range items {
		if it.Source == "upload" && hidden[it.ID] {
			continue
		}
		out = append(out, it)
	}
	return out
}
