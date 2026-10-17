package routes

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cdknorow/coral/internal/board"
)

// managedArtifactURI is a link to an object in Coral's artifact store, in any of the forms an agent
// writes it: coral://artifacts/<hex> or /api/artifacts/<hex>, either case, an optional ?v=N, spaces
// around it. Exactly 64 hex digits: 63 or 65 is not a managed link at all.
var managedArtifactURI = regexp.MustCompile(`^(?i:coral://artifacts/|/api/artifacts/)([a-fA-F0-9]{64})(?:\?v=[0-9]+)?$`)

// managedArtifactID is the lower-case object id a managed link names, or "".
func managedArtifactID(uri string) string {
	if m := managedArtifactURI.FindStringSubmatch(strings.TrimSpace(uri)); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// Task artifact provenance (#4379, Security's spec): how a managed link in a task result was
// checked when it was recorded. A display value only: what --attach may send is decided from
// upload records at the time of the send, never from this.
const (
	provenanceUploaded   = "uploaded"   // the object is there and the team has an upload record of it
	provenancePerson     = "person"     // recorded by the Operator or a Coral Hub user, and the object is there
	provenanceUnverified = "unverified" // a managed-looking link the check did not pass: listed as a plain link
)

// vetTaskArtifacts checks every managed link in a task result before it is recorded (#4379): an
// agent's counts as one of the team's artifacts only if the object exists and the id is in an upload
// record of the caller's own session or of a session on the board (so an agent can't make another
// team's file its own by naming its id); a person's only needs the object to exist. A link that
// fails is kept, as the agent wrote it, marked unverified. Any provenance the client sent is
// cleared first. It returns the names of the links it marked unverified, each also logged.
func (h *BoardHandler) vetTaskArtifacts(r *http.Request, project string, artifacts []board.TaskArtifact) []string {
	if len(artifacts) == 0 {
		return nil
	}
	actor := callerActor(r)
	person := actor.Kind == ActorOperator || actor.Kind == ActorHub
	var sessions []string
	if !person {
		if owner, ok := VerifiedSession(r.Context()); ok && owner != nil && owner.SessionID != "" {
			sessions = append(sessions, owner.SessionID)
		}
		if subs, err := h.bs.TeamSubscriberSessions(r.Context(), project); err == nil {
			for _, sid := range subs {
				if sid != "" {
					sessions = append(sessions, sid)
				}
			}
		}
	}
	var unverified []string
	for i := range artifacts {
		a := &artifacts[i]
		a.Provenance = ""
		id := managedArtifactID(a.URI)
		if id == "" {
			continue
		}
		switch {
		case !h.objectIsStored(id):
			a.Provenance = provenanceUnverified
		case person:
			a.Provenance = provenancePerson
		case h.uploadedByAny(id, sessions) || h.receivedOn(project, id):
			a.Provenance = provenanceUploaded
		default:
			a.Provenance = provenanceUnverified
		}
		if a.Provenance == provenanceUnverified {
			unverified = append(unverified, a.Name)
			slog.Warn("task artifact link not verified: kept as a plain link", "board", project, "artifact", a.Name, "id", id, "caller", actor.Kind)
		}
	}
	return unverified
}

// objectIsStored reports whether the store holds id as a regular file (not a link to one).
func (h *BoardHandler) objectIsStored(id string) bool {
	dir := h.artifactObjectDir()
	if dir == "" {
		return false
	}
	st, err := os.Lstat(filepath.Join(dir, id))
	return err == nil && st.Mode().IsRegular()
}

// objectSidecarDigest is the digest the object's metadata sidecar records ("" when unreadable).
func (h *BoardHandler) objectSidecarDigest(id string) string {
	dir := h.artifactObjectDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, id+".json")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxArtifactMetaBytes {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var meta agentArtifactMeta
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.Digest
}

// uploadRecordOf is sessionID's upload record of id when it is a genuine one: its file name and its
// digest field both name id, and the object's sidecar agrees. A record any process of this user
// wrote by hand into an uploads folder passes only if the object and its sidecar really match.
func (h *BoardHandler) uploadRecordOf(sessionID, id string) (agentUploadRecord, bool) {
	for _, u := range h.uploads.list(h.coralDir, sessionID) {
		if u.id != id {
			continue
		}
		if !strings.EqualFold(u.rec.Digest, "sha256:"+id) || !strings.EqualFold(h.objectSidecarDigest(id), "sha256:"+id) {
			return agentUploadRecord{}, false
		}
		return u.rec, true
	}
	return agentUploadRecord{}, false
}

// uploadedByAny reports whether any of sessions has a genuine upload record of id.
func (h *BoardHandler) uploadedByAny(id string, sessions []string) bool {
	for _, sid := range sessions {
		if _, ok := h.uploadRecordOf(sid, id); ok {
			return true
		}
	}
	return false
}

// receivedOn reports whether board project received id with a cross-team message (its received
// record): citable by that board's tasks, never attachable onward. Filled in with the received
// records (#4379); none yet.
func (h *BoardHandler) receivedOn(project, id string) bool { return false }
