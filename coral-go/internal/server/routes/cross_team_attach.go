package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/secfile"
)

// Per-send limits for coral-board send --attach (#4379, coral-hub's design section 10 C1).
const (
	maxCrossTeamAttachments = 4
	maxCrossTeamAttachBytes = 8 << 20  // one artifact
	maxCrossTeamAttachTotal = 16 << 20 // all of one send's
)

// attachRefusal is why a send's attachments were refused: an HTTP status, a fixed code and words.
type attachRefusal struct {
	status    int
	code, msg string
}

func (e *attachRefusal) write(w http.ResponseWriter) {
	if e.status == http.StatusTooManyRequests {
		// The hourly attach limit limits one action, not the server: marked, so the dashboard's pollers do
		// not pause on it (the 429 census, #3434). Same body as every other refusal.
		httputil.WriteActionLimit(w, 0, e.code, e.msg)
		return
	}
	writeJSON(w, e.status, map[string]string{"code": e.code, "error": e.msg})
}

// notYours is the one answer for an id that is not attachable by an agent, whatever the reason (it
// does not exist, another team stored it, only a task result or a received or shared row names it):
// --attach must not tell an agent what other teams hold (coral-hub's design section 2).
func notYours(raw string) *attachRefusal {
	return &attachRefusal{http.StatusBadRequest, "not_yours", fmt.Sprintf("%s is not one of your team's uploads: attach only a file an agent of your team uploaded with coral-agent artifact upload", raw)}
}

// attachSender is who attaches: a person (the Operator, at the dashboard, the signed command line or
// through Coral Hub) may send any stored object; an agent what an agent of its team uploaded.
type attachSender struct {
	person    bool
	sessionID string // the agent's verified session (empty: it can attach nothing)
	// team are the sessions whose uploads count, with when each joined the sending board: an upload
	// counts only if it was made on or after that (an agent that moved boards doesn't bring an
	// earlier board's uploads; a past member's uploads from its time here still count).
	team      map[string]time.Time
	windowKey string // its hourly upload window
}

// attachedArtifact is one checked attachment.
type attachedArtifact struct {
	ID, Name, MediaType string
	Size                int64
}

// attachBeforeHash, when set (tests), runs after an attachment's object is open and checked and
// before its bytes are read.
var attachBeforeHash func(path string)

// resolveAttachments checks a send's --attach ids, and reads each one through (#4379): for an agent
// the id must be in a genuine upload record of a session of the sending board's team at the time of
// the send (the Orchestrator's decision, 11:42: the team's uploads, as Team Artifacts lists them;
// never a task result, a shared or a received row, or a stored flag); for a person, a stored object.
// Each object is opened ONCE, no-follow, and everything uses that handle: fstat (a regular file,
// within the size cap, the record's size), the sidecar's digest, then the bytes, hashed on the way
// and compared with the id at the end, so a file deleted or rewritten in place between the check and
// the read fails the send. Within the per-send limits and the sender's hourly upload window.
func (h *BoardHandler) resolveAttachments(raw []string, s attachSender) ([]attachedArtifact, *attachRefusal) {
	if len(raw) == 0 {
		return nil, nil
	}
	var ids []string
	seen := map[string]bool{}
	for _, a := range raw {
		id := managedArtifactID(a)
		if id == "" {
			return nil, &attachRefusal{http.StatusBadRequest, "bad_attach", fmt.Sprintf("--attach takes a coral://artifacts/<id> link, not %q", strings.TrimSpace(a))}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > maxCrossTeamAttachments {
		return nil, &attachRefusal{http.StatusBadRequest, "attach_too_many", fmt.Sprintf("at most %d attachments per send", maxCrossTeamAttachments)}
	}
	dir := h.artifactObjectDir()
	if dir == "" {
		return nil, &attachRefusal{http.StatusServiceUnavailable, "artifacts_unavailable", "this Coral has no artifact store"}
	}
	st := storeFor(h.coralDir)
	var out []attachedArtifact
	var total int64
	for _, id := range ids {
		shown := "coral://artifacts/" + id
		var rec agentUploadRecord
		if !s.person {
			var ok bool
			if s.sessionID == "" {
				return nil, notYours(shown)
			}
			if rec, ok = h.teamUploadRecordOf(s.team, id); !ok {
				return nil, notYours(shown)
			}
		}
		if ok, retry := st.admitUpload(s.windowKey); !ok {
			return nil, &attachRefusal{http.StatusTooManyRequests, "attach_limit", fmt.Sprintf("the hourly upload limit is used up; try again in %s", retry.Round(time.Minute))}
		}
		a, refused := h.readAttachment(dir, id, shown, s, rec)
		if refused != nil {
			return nil, refused
		}
		if !st.chargeUpload(s.windowKey, a.Size) {
			return nil, &attachRefusal{http.StatusTooManyRequests, "attach_limit", "the hourly upload limit is used up"}
		}
		total += a.Size
		if total > maxCrossTeamAttachTotal {
			return nil, &attachRefusal{http.StatusRequestEntityTooLarge, "attach_total_too_large", fmt.Sprintf("the attachments are over %d MiB together, the limit for one send", maxCrossTeamAttachTotal>>20)}
		}
		out = append(out, a)
	}
	return out, nil
}

// readAttachment opens id once and checks it through that handle (see resolveAttachments).
func (h *BoardHandler) readAttachment(dir, id, shown string, s attachSender, rec agentUploadRecord) (attachedArtifact, *attachRefusal) {
	missing := func() *attachRefusal {
		if s.person {
			return &attachRefusal{http.StatusNotFound, "attach_not_found", fmt.Sprintf("%s is not in this Coral's artifact store", shown)}
		}
		return notYours(shown)
	}
	path := filepath.Join(dir, id)
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return attachedArtifact{}, missing()
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return attachedArtifact{}, missing()
	}
	if info.Size() > maxCrossTeamAttachBytes {
		return attachedArtifact{}, &attachRefusal{http.StatusRequestEntityTooLarge, "attach_too_large", fmt.Sprintf("%s is over %d MiB, the limit for one attachment", shown, maxCrossTeamAttachBytes>>20)}
	}
	if !s.person && info.Size() != rec.Size {
		return attachedArtifact{}, notYours(shown)
	}
	if !strings.EqualFold(h.objectSidecarDigest(id), "sha256:"+id) {
		return attachedArtifact{}, missing()
	}
	if attachBeforeHash != nil {
		attachBeforeHash(path)
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(f, maxCrossTeamAttachBytes+1))
	if err != nil || n != info.Size() || hex.EncodeToString(sum.Sum(nil)) != id {
		return attachedArtifact{}, &attachRefusal{http.StatusConflict, "attach_changed", fmt.Sprintf("%s changed while it was being sent; nothing was sent", shown)}
	}
	a := attachedArtifact{ID: id, Name: rec.Name, MediaType: rec.MediaType, Size: n}
	if a.Name == "" || a.MediaType == "" {
		if meta, ok := h.objectMeta(id); ok {
			if a.Name == "" {
				a.Name = meta.Name
			}
			if a.MediaType == "" {
				a.MediaType = meta.MediaType
			}
		}
	}
	return a, nil
}

// teamUploadRecordOf is a genuine upload record of id by one of the team's sessions, made on or
// after that session joined the board (sessions are tried in a fixed order).
func (h *BoardHandler) teamUploadRecordOf(team map[string]time.Time, id string) (agentUploadRecord, bool) {
	sids := make([]string, 0, len(team))
	for sid := range team {
		sids = append(sids, sid)
	}
	sort.Strings(sids)
	for _, sid := range sids {
		rec, ok := h.uploadRecordOf(sid, id)
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, rec.CreatedAt)
		if err == nil && !at.Before(team[sid]) {
			return rec, true
		}
	}
	return agentUploadRecord{}, false
}

// teamSessions are the agent sessions of project's team (every subscriber's, and every session that
// claimed one of its tasks) with when each joined the board (board.SessionJoinTimes).
func (h *BoardHandler) teamSessions(ctx context.Context, project string) map[string]time.Time {
	team, err := h.bs.SessionJoinTimes(ctx, project)
	if err != nil {
		return nil
	}
	return team
}

// objectMeta is an object's sidecar metadata.
func (h *BoardHandler) objectMeta(id string) (agentArtifactMeta, bool) {
	var meta agentArtifactMeta
	dir := h.artifactObjectDir()
	if dir == "" {
		return meta, false
	}
	path := filepath.Join(dir, id+".json")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxArtifactMetaBytes {
		return meta, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		return meta, false
	}
	return meta, true
}

// attachmentLines are the lines appended to a message that carries attachments, so the receiving
// Orchestrator sees what came with it and where (its Team Artifacts list them too).
func attachmentLines(atts []attachedArtifact) string {
	if len(atts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAttached (in Team Artifacts):")
	for _, a := range atts {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = "artifact"
		}
		fmt.Fprintf(&b, "\n- %s: coral://artifacts/%s", name, a.ID)
	}
	return b.String()
}

// ── Shared and received records ─────────────────────────────────────────

// crossTeamArtifactRecord is one artifact a board sent ("shared", on the sender) or received
// ("received", on the receiver) with a cross-team message (#4379). It names a send, never an upload:
// neither kind makes the artifact attachable onward (v1) nor counts as the team's upload.
type crossTeamArtifactRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type,omitempty"`
	Size       int64  `json:"size"`
	Board      string `json:"board"`       // the board this record is on
	OtherBoard string `json:"other_board"` // shared: the board it went to; received: the board it came from
	Server     string `json:"server"`      // the other board's Coral ("local": this one)
	By         string `json:"by"`          // who sent it
	MessageID  int64  `json:"message_id"`  // the message it came with, on this board
	At         string `json:"at"`
}

// linkFile is os.Link, replaceable in tests (a folder that can't hard-link).
var linkFile = os.Link

// crossTeamRecordKinds are the folders of cross-team artifact rows under <coralDir>/artifacts.
var crossTeamRecordKinds = []string{"shared", "received"}

// boardDirName is a board's folder name under them: 32 hex digits (a hash of the board's name).
var boardDirName = regexp.MustCompile(`^[0-9a-f]{32}$`)

// boardRecordDir is <coralDir>/artifacts/<kind>/<a hash of the board>: board names are free text.
func (h *BoardHandler) boardRecordDir(kind, board string) string {
	if h.coralDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(board))
	return filepath.Join(h.coralDir, "artifacts", kind, hex.EncodeToString(sum[:16]))
}

// writeCrossTeamRecord stores rec as <dir>/<id>.json. A shared row is replaced by a newer send of the
// same object; a received row is write-once (Security's C1 review (b)): the first receipt's
// provenance stands, and a later one never rewrites where the file came from.
func (h *BoardHandler) writeCrossTeamRecord(kind string, rec crossTeamArtifactRecord) error {
	dir := h.boardRecordDir(kind, rec.Board)
	if dir == "" {
		return errors.New("no data folder")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rec-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	final := filepath.Join(dir, rec.ID+".json")
	if kind == "received" {
		// A hard link fails if the name exists, so of two receipts at once only one wins.
		defer os.Remove(tmp.Name())
		err := linkFile(tmp.Name(), final)
		if err == nil || errors.Is(err, os.ErrExist) {
			return nil
		}
		// A folder that can't hard-link (FAT, exFAT, some network shares: Security's C1 INFO 1):
		// create the final name exclusively instead, so the first receipt still wins and is kept.
		f, cerr := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(cerr, os.ErrExist) {
			return nil
		}
		if cerr != nil {
			return cerr
		}
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(final)
			return werr
		}
		return f.Close()
	}
	return os.Rename(tmp.Name(), final)
}

// crossTeamRecords lists a board's records of one kind (nil when none).
func (h *BoardHandler) crossTeamRecords(kind, board string) []crossTeamArtifactRecord {
	dir := h.boardRecordDir(kind, board)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []crossTeamArtifactRecord
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || id == e.Name() || managedArtifactID("coral://artifacts/"+id) != id {
			continue
		}
		if rec, ok := h.crossTeamRecord(kind, board, id); ok {
			out = append(out, rec)
		}
	}
	return out
}

// mergeCrossTeamRows adds a board's shared and received rows to its Team Artifacts (#4379): each is
// a reference to the stored object (size, type, preview and download come from the store, as for any
// managed row), listed apart from the team's own uploads and task results (its id says which kind),
// and never makes the object one of the team's uploads.
func (h *BoardHandler) mergeCrossTeamRows(items []teamArtifact, project string) []teamArtifact {
	added := false
	for _, kind := range crossTeamRecordKinds {
		for _, rec := range h.crossTeamRecords(kind, project) {
			// No producer and no session: the row names a send, not one of this team's agents' work.
			item := teamArtifact{
				ID: kind + ":" + rec.ID, Name: rec.Name, MediaType: rec.MediaType, CreatedAt: rec.At,
				Source: kind, URI: "coral://artifacts/" + rec.ID, ReferenceCount: 1, managed: rec.ID,
				SentBy: rec.By,
			}
			where := rec.OtherBoard
			if rec.Server != "" && rec.Server != "local" {
				where = rec.Server + "/" + rec.OtherBoard // across Corals (C2/C3)
			}
			if kind == "shared" {
				item.SharedTo = where
			} else {
				item.ReceivedFrom, item.FromBoard = where, rec.OtherBoard
			}
			items = append(items, item)
			added = true
		}
	}
	if added {
		sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	}
	return items
}

// crossTeamRecord is board's record of id, of one kind, when there is a well-formed one for it.
func (h *BoardHandler) crossTeamRecord(kind, board, id string) (crossTeamArtifactRecord, bool) {
	var rec crossTeamArtifactRecord
	dir := h.boardRecordDir(kind, board)
	if dir == "" {
		return rec, false
	}
	path := filepath.Join(dir, id+".json")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxArtifactMetaBytes {
		return rec, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &rec) != nil || rec.ID != id || rec.Board != board {
		return crossTeamArtifactRecord{}, false
	}
	return rec, true
}
