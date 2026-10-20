package routes

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Deleting the received files no board lists (#4441, Security's #4440 INFO 2): since #4440 a received file waits for its message, and one whose
// message never arrives stays stored and unlisted. They count toward the received-files cap and Settings > Storage lists them; the Operator may
// delete them, by exact digest, with a confirmation. Never automatic, and never a file anything lists or links.

const maxUnlistedDeleteIDs = 100

type unlistedSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// deleteUnlistedReceivedRoute: POST /api/artifacts/received/unlisted/delete {"ids": ["<64 hex>", ...]}. Operator only (a person: the dashboard or
// the signed command line), never through Coral Hub. Each id is re-checked as it is deleted; one that is not (or no longer) an unlisted
// received file is skipped with the reason and left alone.
func (h *SessionsHandler) deleteUnlistedReceivedRoute(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "delete received files") {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSONLimit(r, &body, 64*1024); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > maxUnlistedDeleteIDs {
		errBadRequest(w, "ids must name between 1 and 100 files")
		return
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range body.IDs {
		if managedArtifactID("coral://artifacts/"+id) != id {
			errBadRequest(w, "each id must be a 64-digit lower-case hex digest")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), pruneRunTimeout)
	defer cancel()
	refs, err := h.scanArtifactReferences(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "scan_failed", "could not check what links these files, so nothing was deleted: "+err.Error())
		return
	}
	var freed int64
	deleted := 0
	skipped := []unlistedSkip{}
	for _, id := range ids {
		n, ok, reason := h.deleteUnlistedReceived(ctx, id, refs)
		if ok {
			deleted++
			freed += n
		} else {
			skipped = append(skipped, unlistedSkip{ID: id, Reason: reason})
		}
	}
	slog.Info("[artifacts] unlisted received files deleted by the Operator", "deleted", deleted, "bytes", freed, "skipped", len(skipped))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "bytes": freed, "skipped": skipped})
}

// deleteUnlistedReceived deletes one file only if it is still a received file nothing lists: it has pending receipts and every one has expired,
// no board lists it (received or shared), nothing durable links it, no agent uploaded the same bytes here, and the Operator did not keep it. The
// checks are made under the digest's lock, which an upload of the same bytes also takes.
func (h *SessionsHandler) deleteUnlistedReceived(ctx context.Context, id string, refs map[string]bool) (int64, bool, string) {
	dir := h.cfg.CoralDir()
	defer lockArtifact(id)()
	paths := pendingRecordPaths(dir, id)
	if len(paths) == 0 {
		return 0, false, "not a received file waiting for a message"
	}
	now := time.Now()
	for _, path := range paths {
		p, ok := readPending(path)
		if !ok {
			continue
		}
		if at, err := time.Parse(time.RFC3339, p.Rec.At); err == nil && now.Sub(at) <= receivedPendingFor {
			return 0, false, "still waiting for its message"
		}
	}
	// A board may have listed it since the scan: look again at the rows, which is cheap.
	fresh := map[string]bool{}
	if err := scanCrossTeamRecordRefs(dir, fresh); err != nil || refs[id] || fresh[id] {
		return 0, false, "a board lists it, or something names it"
	}
	if hasUploadRecord(dir, id) {
		return 0, false, "an agent uploaded the same file here"
	}
	if kept, err := h.isKept(ctx, id); err != nil || kept {
		return 0, false, "kept"
	}
	freed, _, found, err := deleteArtifactFilesLocked(dir, id)
	for _, path := range paths {
		_ = os.Remove(path)
	}
	if err != nil {
		return 0, false, "could not delete it"
	}
	if !found {
		return 0, false, "already gone"
	}
	h.dropArtifactComments(ctx, id)
	return freed, true, ""
}
