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

func (h *SessionsHandler) uploadAgentArtifact(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if _, ok := h.agentFromRequest(w, r, sid); !ok {
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
	digestBytes := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	if err := os.MkdirAll(h.artifactDir(), 0o755); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	objectPath := filepath.Join(h.artifactDir(), hex.EncodeToString(digestBytes[:]))
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		if err := os.WriteFile(objectPath, data, 0o644); err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}
	meta := agentArtifactMeta{Name: name, MediaType: mediaType, Size: int64(len(data)), Digest: digest}
	metaData, _ := json.Marshal(meta)
	if err := os.WriteFile(objectPath+".json", metaData, 0o644); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if dir := uploadsDir(h.cfg.CoralDir(), sid); dir != "" {
		if os.MkdirAll(dir, 0o755) == nil {
			rec, _ := json.Marshal(agentUploadRecord{meta, time.Now().UTC().Format(time.RFC3339)})
			_ = os.WriteFile(filepath.Join(dir, hex.EncodeToString(digestBytes[:])+".json"), rec, 0o644)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"uri":        "coral://artifacts/" + hex.EncodeToString(digestBytes[:]),
		"url":        "/api/artifacts/" + hex.EncodeToString(digestBytes[:]),
		"name":       name,
		"media_type": mediaType,
		"size":       len(data),
		"digest":     digest,
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
	path := filepath.Join(h.artifactDir(), id)
	meta := agentArtifactMeta{}
	metaData, err := os.ReadFile(path + ".json")
	if err != nil || json.Unmarshal(metaData, &meta) != nil {
		errNotFound(w, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", meta.MediaType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(meta.Name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}
