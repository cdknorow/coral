package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/mediatype"
	"github.com/go-chi/chi/v5"
)

const maxAgentArtifactSize = 64 << 20

type agentArtifactMeta struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

// agentUploadRecord is a per-session record of an upload, so an agent's
// Artifacts tab lists what it uploaded before (or without) attaching it to a
// task result.
type agentUploadRecord struct {
	agentArtifactMeta
	CreatedAt string `json:"created_at"`
}

// uploadsDir is where per-session upload records live. The session id is
// rejected unless it is a plain name, so it cannot escape the directory.
func uploadsDir(coralDir, sessionID string) string {
	if coralDir == "" || sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, "/\\\x00") || strings.Contains(sessionID, "..") {
		return ""
	}
	return filepath.Join(coralDir, "artifacts", "uploads", sessionID)
}

// RegisterAgentArtifacts adds Coral-managed, durable artifact storage. Uploads
// are authenticated through the calling agent session; downloads use the
// opaque digest ID so task hand-offs remain reachable by other agents/users.
func (h *SessionsHandler) RegisterAgentArtifacts(r chi.Router) {
	r.Post("/api/agent/artifacts", h.uploadAgentArtifact)
	r.Get("/api/artifacts/{id}", h.getAgentArtifact)
}

func (h *SessionsHandler) artifactDir() string {
	return filepath.Join(h.cfg.CoralDir(), "artifacts", "objects")
}

// storeArtifact writes data as a content-addressed artifact object under coralDir (the
// object, its metadata sidecar, and the session's upload record, so the agent's
// Artifacts tab lists it) and returns its hex id and the artifact's stored metadata. Shared
// by coral-agent artifact upload and the board's auto-attach (#3515).
//
// #3552 (Security's #3551 audit): the first writer's name and type stay. The sidecar is
// created once (an exclusive hard link from a temp file) and never rewritten, so uploading
// the same bytes again can't relabel someone else's artifact; each uploader's own name is
// kept in its uploads/<session>/<hex>.json record. The object is written to a temp file and
// renamed into place before the sidecar exists, so a sidecar never points at a partial
// object. Folders are 0700 and files 0600.
func storeArtifact(coralDir, sessionID, name, mediaType string, data []byte) (string, agentArtifactMeta, error) {
	digestBytes := sha256.Sum256(data)
	hexID := hex.EncodeToString(digestBytes[:])
	mine := agentArtifactMeta{Name: name, MediaType: mediaType, Size: int64(len(data)), Digest: "sha256:" + hexID}
	dir := filepath.Join(coralDir, "artifacts", "objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", mine, err
	}
	objectPath := filepath.Join(dir, hexID)
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		if err := writeFileAtomic(objectPath, data, 0o600); err != nil {
			return "", mine, err
		}
	}
	stored, err := createSidecarOnce(objectPath+".json", mine)
	if err != nil {
		return "", mine, err
	}
	if up := uploadsDir(coralDir, sessionID); up != "" {
		if os.MkdirAll(up, 0o700) == nil {
			rec, _ := json.Marshal(agentUploadRecord{mine, time.Now().UTC().Format(time.RFC3339)})
			_ = writeFileAtomic(filepath.Join(up, hexID+".json"), rec, 0o600)
		}
	}
	return hexID, stored, nil
}

// writeFileAtomic writes data to a temp file in path's folder and renames it into place.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// createSidecarOnce stores meta as the artifact's sidecar unless one exists, and returns
// the sidecar's metadata (the first writer's). The sidecar appears whole or not at all: a
// complete temp file is hard-linked to the name, which fails if the name exists.
func createSidecarOnce(path string, meta agentArtifactMeta) (agentArtifactMeta, error) {
	if existing, ok := readSidecar(path); ok {
		return existing, nil
	}
	data, _ := json.Marshal(meta)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return meta, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return meta, err
	}
	if err := tmp.Close(); err != nil {
		return meta, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return meta, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if existing, ok := readSidecar(path); ok { // another upload of the same bytes won
			return existing, nil
		}
		return meta, err
	}
	return meta, nil
}

func readSidecar(path string) (agentArtifactMeta, bool) {
	var m agentArtifactMeta
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &m) != nil || m.Digest == "" {
		return m, false
	}
	return m, true
}

func (h *SessionsHandler) uploadAgentArtifact(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if _, ok := h.agentFromRequest(w, r, &sid, false); !ok {
		return
	}
	name := strings.TrimSpace(r.Header.Get("X-Artifact-Name"))
	if name == "" || len(name) > 160 || strings.ContainsAny(name, "\x00\r\n") {
		errBadRequest(w, "X-Artifact-Name must be 1–160 characters")
		return
	}
	mediaType := mediatype.Resolve(r.Header.Get("X-Artifact-Media-Type"), name)
	if mediaType == "" {
		mediaType = mediatype.Generic
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentArtifactSize+1)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		errBadRequest(w, "artifact exceeds 64 MiB")
		return
	}
	if len(data) > maxAgentArtifactSize {
		errBadRequest(w, "artifact exceeds 64 MiB")
		return
	}
	hexID, meta, err := storeArtifact(h.cfg.CoralDir(), sid, name, mediaType, data)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// The name and type viewers get: the first upload's, when these bytes were stored before.
	writeJSON(w, http.StatusCreated, map[string]any{
		"uri":        "coral://artifacts/" + hexID,
		"url":        artifactURL(hexID),
		"name":       meta.Name,
		"media_type": meta.MediaType,
		"size":       len(data),
		"digest":     meta.Digest,
	})
}

func (h *SessionsHandler) getAgentArtifact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if len(id) != 64 {
		errBadRequest(w, "invalid artifact id")
		return
	}
	if _, err := hex.DecodeString(id); err != nil {
		errBadRequest(w, "invalid artifact id")
		return
	}
	// The link form (#3168): /api/artifacts/<digest>?v=2. The tunnel accepts exactly that, so the
	// handler does too: v absent or 2, nothing else (#3179).
	if q := r.URL.Query(); q.Has("v") && (len(q["v"]) != 1 || q.Get("v") != "2") {
		errBadRequest(w, "invalid artifact link version")
		return
	}
	path := filepath.Join(h.artifactDir(), id)
	meta := agentArtifactMeta{}
	metaData, err := os.ReadFile(path + ".json")
	if err != nil || json.Unmarshal(metaData, &meta) != nil {
		errNotFound(w, "artifact not found")
		return
	}
	// An agent chose these bytes and their type: an HTML or SVG upload must not
	// run on the dashboard's origin when someone opens its link.
	setUntrustedContentHeaders(w.Header(), meta.MediaType, meta.Name)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}
