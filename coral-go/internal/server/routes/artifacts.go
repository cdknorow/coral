package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/mediatype"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/internal/store"
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
	// One safe path element: [A-Za-z0-9_-], at most 128, starting with a letter or digit, so
	// never ".", "..", a separator, a drive ("C:") or a Windows device-like name (#3602 W26).
	if coralDir == "" || !safefs.IsSafeID(sessionID) {
		return ""
	}
	return filepath.Join(coralDir, "artifacts", "uploads", sessionID)
}

// RegisterAgentArtifacts adds Coral-managed, durable artifact storage. Uploads
// are authenticated through the calling agent session; downloads use the
// opaque digest ID so task hand-offs remain reachable by other agents/users.
func (h *SessionsHandler) RegisterAgentArtifacts(r chi.Router) {
	// The store's size is measured now, before any upload (#3553), and capped by the setting.
	st := storeFor(h.cfg.CoralDir())
	st.mu.Lock()
	st.maxBytes = h.artifactStoreMaxBytes
	st.receivedMaxBytes = h.receivedArtifactsMaxBytes
	st.mu.Unlock()
	r.Post("/api/agent/artifacts", h.uploadAgentArtifact)
	r.Get("/api/artifacts/{id}", h.getAgentArtifact)
	r.Delete("/api/artifacts/{id}", h.deleteAgentArtifact)
	r.Post("/api/artifacts/received/unlisted/delete", h.deleteUnlistedReceivedRoute) // #4441
	r.Get("/api/system/artifact-store/kept", h.keptListRoute)                        // #3554
	r.Post("/api/artifacts/prune", h.pruneArtifactsRoute)
	r.Post("/api/artifacts/retention/ack", h.retentionAckRoute) // #3554
	r.Put("/api/artifacts/{id}/keep", h.keepArtifactRoute)      // #3554
	r.Delete("/api/artifacts/{id}/keep", h.keepArtifactRoute)   // #3554
	r.Get("/api/system/artifact-store", h.getArtifactStore)
}

// ArtifactStoreMaxBytesSetting caps the artifact store's size in bytes (#3553). Unset (or blank) is the default,
// defaultArtifactStoreMaxBytes (#4602: a store with no cap grows until the disk is full); a stored 0 is no cap, the Operator's choice.
const ArtifactStoreMaxBytesSetting = "artifact_store_max_bytes"

// defaultArtifactStoreMaxBytes is the cap when the Operator has set none: 20 GiB. Above it new uploads are refused, nothing is
// deleted (#4602, the Orchestrator's decision 2); Settings > Storage shows the store from 80% of it.
const defaultArtifactStoreMaxBytes int64 = 20 << 30

// artifactStoreRefuse is the cap when the setting can't be read: one byte, so the store takes nothing new
// rather than having no cap (#4432, Security's #4431 SHOULD 2).
const artifactStoreRefuse int64 = 1

// ArtifactStoreAutoRaisedSetting is Coral's own record that it raised the store's limit at startup (RaiseArtifactStoreLimitIfOver): the
// limit it wrote, in bytes. Settings > Storage shows a note while the saved limit is still that number, and says nothing once the Operator
// has chosen another.
const ArtifactStoreAutoRaisedSetting = "artifact_store_auto_raised"

// RaiseArtifactStoreLimitIfOver is run once at startup (#4602, the Orchestrator's rule for the 20 GiB default). A Coral that has no limit
// set and a store already at 80% of the default (16 GiB) or more would start refusing uploads the moment it upgraded, with nobody
// watching: so the limit is written once, as twice the store's size rounded up to a whole GiB, and logged. It writes a setting and
// deletes nothing; an Operator's own value (0 included) is never changed. It reports the limit it wrote, 0 when it wrote none.
func RaiseArtifactStoreLimitIfOver(ctx context.Context, ss *store.SessionStore, coralDir string) int64 {
	if ss == nil {
		return 0
	}
	settings, err := ss.GetSettings(ctx)
	if err != nil || strings.TrimSpace(settings[ArtifactStoreMaxBytesSetting]) != "" {
		return 0
	}
	used, _, _ := storeFor(coralDir).size()
	if used*5 < defaultArtifactStoreMaxBytes*4 {
		return 0
	}
	const gib = int64(1) << 30
	limit := (2*used + gib - 1) / gib * gib
	if err := ss.SetSetting(ctx, ArtifactStoreMaxBytesSetting, strconv.FormatInt(limit, 10)); err != nil {
		slog.Warn("artifact store: could not raise its limit above the new default", "used", used, "error", err)
		return 0
	}
	if err := ss.SetSetting(ctx, ArtifactStoreAutoRaisedSetting, strconv.FormatInt(limit, 10)); err != nil {
		slog.Warn("artifact store: raised its limit but could not record it", "error", err)
	}
	slog.Info("artifact store: it already held more than 80% of the new 20 GiB default, so its limit was raised once to twice its size; change it in Settings > Storage",
		"used", humanBytes(used), "limit", humanBytes(limit))
	return limit
}

func (h *SessionsHandler) artifactStoreMaxBytes() int64 {
	if h.ss == nil {
		return 0
	}
	s, err := h.ss.GetSettings(context.Background())
	if err != nil {
		return artifactStoreRefuse
	}
	if strings.TrimSpace(s[ArtifactStoreMaxBytesSetting]) == "" {
		return defaultArtifactStoreMaxBytes
	}
	n, ok := parseStoreCap(s[ArtifactStoreMaxBytesSetting])
	if !ok {
		slog.Warn("artifact_store_max_bytes is not a whole number of bytes; the store takes nothing new until it is set again", "value", s[ArtifactStoreMaxBytesSetting])
		return artifactStoreRefuse
	}
	return n
}

// parseStoreCap reads the cap: empty is 0 (no cap); a whole number of bytes, also in the exponent form older
// versions stored a large one in ("1.073741824e+10"). ok is false for anything else.
func parseStoreCap(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, n >= 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f != math.Trunc(f) || f > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

// getArtifactStore: GET /api/system/artifact-store, the object store's size, object count and
// cap, and, as separate lines, the saved task and session diffs (artifacts/tasks and
// artifacts/sessions), which the cap doesn't cover (#3553; Settings shows them). Not in the
// tunnel table: this computer only.
func (h *SessionsHandler) getArtifactStore(w http.ResponseWriter, r *http.Request) {
	coralDir := h.cfg.CoralDir()
	b, n, max := storeFor(coralDir).size()
	tb, tn, sb, sn := diffStoreSizes(coralDir)
	out := map[string]any{
		"bytes": b, "objects": n, "max_bytes": max,
		"task_diffs":    map[string]int64{"bytes": tb, "files": tn},
		"session_diffs": map[string]int64{"bytes": sb, "files": sn},
	}
	// The limit Coral raised by itself at startup (RaiseArtifactStoreLimitIfOver), while it is still the saved limit.
	if h.ss != nil {
		if saved, err := h.ss.GetSettings(r.Context()); err == nil && saved[ArtifactStoreAutoRaisedSetting] != "" && saved[ArtifactStoreAutoRaisedSetting] == saved[ArtifactStoreMaxBytesSetting] {
			if n, err := strconv.ParseInt(saved[ArtifactStoreAutoRaisedSetting], 10, 64); err == nil {
				out["auto_raised"] = n
			}
		}
	}
	out["received"] = h.receivedStatus(isOperatorRequest(r)) // #4441: the total received from other Corals and boards, its cap and the one-time notice; the digests of the unlisted files only for the Operator (#4489)
	// ?detail=1 (#3554): by age, by board, kept and retention, for Settings > Storage.
	if r.URL.Query().Get("detail") == "1" {
		if detail, err := h.storageDetail(r.Context()); err == nil {
			out["detail"] = detail
		} else {
			out["detail_error"] = "could not read the store's details"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

var diffSizes struct {
	sync.Mutex
	dir    string
	at     time.Time
	tb, tn int64
	sb, sn int64
}

// diffStoreSizes walks artifacts/tasks and artifacts/sessions (regular files only, no links
// followed), at most every 30 s.
func diffStoreSizes(coralDir string) (tb, tn, sb, sn int64) {
	diffSizes.Lock()
	defer diffSizes.Unlock()
	if diffSizes.dir == coralDir && time.Since(diffSizes.at) < 30*time.Second {
		return diffSizes.tb, diffSizes.tn, diffSizes.sb, diffSizes.sn
	}
	// A diff shared by several tasks or sessions is one file with several hard links (#3555's
	// dedupe): its bytes count once, under the first place it's found; files counts the
	// task and session diffs (the links). QA's #3555 finding.
	seen := map[string]bool{}
	walk := func(dir string) (bytes, files int64) {
		_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if info, ierr := d.Info(); ierr == nil {
				files++
				if key, ok := fileIdentity(info); ok {
					if seen[key] {
						return nil
					}
					seen[key] = true
				}
				bytes += info.Size()
			}
			return nil
		})
		return
	}
	tb, tn = walk(filepath.Join(coralDir, "artifacts", "tasks"))
	sb, sn = walk(filepath.Join(coralDir, "artifacts", "sessions"))
	diffSizes.dir, diffSizes.at = coralDir, time.Now()
	diffSizes.tb, diffSizes.tn, diffSizes.sb, diffSizes.sn = tb, tn, sb, sn
	return
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
	defer lockArtifact(hexID)() // a delete of the same digest waits (#3553)
	mine := agentArtifactMeta{Name: name, MediaType: mediaType, Size: int64(len(data)), Digest: "sha256:" + hexID}
	dir := filepath.Join(coralDir, "artifacts", "objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", mine, err
	}
	objectPath := filepath.Join(dir, hexID)
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		// The store's cap (#3553) is decided here, for new objects only: bytes already stored
		// are never refused.
		st := storeFor(coralDir)
		if !st.fits(int64(len(data))) {
			return "", mine, errStoreFull
		}
		if err := writeFileAtomic(objectPath, data, 0o600); err != nil {
			return "", mine, err
		}
		st.added(int64(len(data)))
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
	if err := tmp.Sync(); err != nil { // on disk before it gets the real name
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return meta, err
	}
	if err := tmp.Close(); err != nil {
		return meta, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return meta, err
	}
	link := os.Link
	if sidecarLinkHook != nil {
		link = func(string, string) error { return sidecarLinkHook() }
	}
	if err := link(tmp.Name(), path); err != nil {
		if existing, ok := readSidecar(path); ok { // another upload of the same bytes won
			return existing, nil
		}
		if !os.IsExist(err) {
			// A file system without hard links: an exclusive create and a full write.
			return createSidecarExclusive(path, meta, data)
		}
		return meta, err
	}
	return meta, nil
}

// sidecarLinkHook lets a test make the hard link fail, as on a file system without them.
var sidecarLinkHook func() error

// createSidecarExclusive is createSidecarOnce without a hard link: O_EXCL creates the name
// (the first writer wins), and the data is written and synced at once.
func createSidecarExclusive(path string, meta agentArtifactMeta, data []byte) (agentArtifactMeta, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if existing, ok := readSidecar(path); ok {
			return existing, nil
		}
		return meta, err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(path)
		return meta, fmt.Errorf("write sidecar: %v %v %v", werr, serr, cerr)
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
	if name == "" || !board.ValidLabel(name, 160) { // plain text: the name goes into cross-team messages typed into agents' terminals (#4901)
		errBadRequest(w, "X-Artifact-Name must be 1–160 characters of plain text, with no control or formatting characters")
		return
	}
	if !board.ValidLabel(strings.TrimSpace(r.Header.Get("X-Artifact-Media-Type")), 127) {
		errBadRequest(w, "X-Artifact-Media-Type must be at most 127 characters of plain text, with no control or formatting characters")
		return
	}
	mediaType := mediatype.Resolve(r.Header.Get("X-Artifact-Media-Type"), name)
	if mediaType == "" {
		mediaType = mediatype.Generic
	}
	if refuseIfDiskLow(w) {
		return
	}
	coralDir := h.cfg.CoralDir()
	st := storeFor(coralDir)
	// The hourly cap is per agent session (its verified token); token-less uploads, possible
	// only in default mode, share one bucket rather than having none (#3553).
	key := sharedUploadBucket
	if owner, ok := VerifiedSession(r.Context()); ok && owner.SessionID != "" {
		key = "session:" + owner.SessionID
	}
	if ok, retry := st.admitUpload(key); !ok {
		artifactHourlyLimited(w, retry,
			fmt.Sprintf("this agent's upload limit for the hour is used up (%d uploads or %d MiB); try again in %s", uploadWindowCount, uploadWindowBytes>>20, retry.Round(time.Minute)))
		return
	}

	// Streamed into a temp file next to the objects while hashing (no io.ReadAll, #3553).
	dir := filepath.Join(coralDir, "artifacts", "objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		errInternalServer(w, "could not store the artifact")
		return
	}
	tmp, err := os.CreateTemp(dir, artifactUploadTempP+"*")
	if err != nil {
		errInternalServer(w, "could not store the artifact")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // a no-op once it has been renamed into the store
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), http.MaxBytesReader(w, r.Body, maxAgentArtifactSize+1))
	closeErr := tmp.Close()
	withinWindow := st.chargeUpload(key, n) // what was read counts, stored or refused
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) || n > maxAgentArtifactSize {
		artifactRefusal(w, http.StatusRequestEntityTooLarge, "artifact_too_large", "", "artifact exceeds 64 MiB")
		return
	}
	if err != nil || closeErr != nil {
		errBadRequest(w, "the upload was cut short")
		return
	}
	if !withinWindow {
		artifactHourlyLimited(w, 0, // the byte window's reset time isn't tracked here; no Retry-After.
			fmt.Sprintf("this agent's upload limit for the hour is used up (%d MiB); try again later", uploadWindowBytes>>20))
		return
	}
	hexID, size := hex.EncodeToString(hash.Sum(nil)), n

	// An image is recognised by its bytes, whatever its declared type, and stored without its
	// metadata, or refused when its format can't be stripped (finding 5). Its id is then the
	// hash of the stored bytes.
	stripped := false
	if head, herr := readHead(tmpPath, 16); herr == nil && sniffImageFamily(head) != "" {
		data, rerr := os.ReadFile(tmpPath)
		if rerr != nil {
			errInternalServer(w, "could not store the artifact")
			return
		}
		clean, changed, cerr := cleanUploadedImage(data)
		if cerr != nil {
			artifactRefusal(w, http.StatusBadRequest, "image_metadata", "", cerr.Error())
			return
		}
		if changed {
			if werr := os.WriteFile(tmpPath, clean, 0o600); werr != nil {
				errInternalServer(w, "could not store the artifact")
				return
			}
			sum := sha256.Sum256(clean)
			hexID, size, stripped = hex.EncodeToString(sum[:]), int64(len(clean)), true
		}
	}

	meta, fresh, err := storeArtifactFile(coralDir, sid, name, mediaType, tmpPath, hexID, size)
	if errors.Is(err, errStoreFull) {
		b, _, max := st.size()
		artifactRefusal(w, http.StatusRequestEntityTooLarge, "artifact_quota", "store",
			fmt.Sprintf("the artifact store is full (%s of %s): ask the Operator to delete old artifacts or raise the limit (Settings > Storage; artifact_store_max_bytes, 0 for none)", humanBytes(b), humanBytes(max)))
		return
	}
	if err != nil {
		errInternalServer(w, "could not store the artifact")
		return
	}
	_ = fresh
	// The name and type viewers get: the first upload's, when these bytes were stored before.
	writeJSON(w, http.StatusCreated, map[string]any{
		"uri":        "coral://artifacts/" + hexID,
		"url":        artifactURL(hexID),
		"name":       meta.Name,
		"media_type": meta.MediaType,
		"size":       meta.Size, // the stored size: smaller than the file when metadata was removed
		"digest":     meta.Digest,
		"stripped":   stripped, // image metadata was removed: the id is the hash of the stored bytes
	})
}

var errStoreFull = errors.New("artifact store is full")

// storeArtifactFile moves an already hashed temp file (in the objects folder) into the store as
// hexID, with its sidecar and the session's upload record (#3553: the streamed counterpart of
// storeArtifact). An object that is already stored makes it a free duplicate. It refuses a new
// object that would take the store past its cap. fresh reports whether the object is new.
func storeArtifactFile(coralDir, sessionID, name, mediaType, tmpPath, hexID string, size int64) (agentArtifactMeta, bool, error) {
	defer lockArtifact(hexID)()
	mine := agentArtifactMeta{Name: name, MediaType: mediaType, Size: size, Digest: "sha256:" + hexID}
	st := storeFor(coralDir)
	objectPath := filepath.Join(coralDir, "artifacts", "objects", hexID)
	fresh := false
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		if !st.fits(size) {
			return mine, false, errStoreFull
		}
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			return mine, false, err
		}
		if err := os.Rename(tmpPath, objectPath); err != nil {
			return mine, false, err
		}
		st.added(size)
		fresh = true
	}
	// As storeArtifact (#3552): the first writer's sidecar stays; this uploader's name is kept
	// in its own upload record.
	stored, err := createSidecarOnce(objectPath+".json", mine)
	if err != nil {
		// An object without a sidecar can't be served (GET is 404) but would still count
		// against the store: remove the one just added (Security's #3553 review).
		if fresh && os.Remove(objectPath) == nil {
			st.removed(size)
		}
		return mine, false, err
	}
	if up := uploadsDir(coralDir, sessionID); up != "" {
		if os.MkdirAll(up, 0o700) == nil {
			rec, _ := json.Marshal(agentUploadRecord{mine, time.Now().UTC().Format(time.RFC3339)})
			_ = writeFileAtomic(filepath.Join(up, hexID+".json"), rec, 0o600)
		}
	}
	return stored, fresh, nil
}

// readHead reads up to n bytes from the start of path.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:k], nil
}

// artifactRefusal answers an upload Coral won't store, with a code the CLI can tell apart:
// artifact_too_large, artifact_quota (limit "hourly" or "store") or image_metadata.
func artifactRefusal(w http.ResponseWriter, status int, code, limit, msg string) {
	body := map[string]string{"error": msg, "code": code, "message": msg}
	if limit != "" {
		body["limit"] = limit
	}
	writeJSON(w, status, body)
}

// artifactHourlyLimited refuses an upload that hit an hourly rate cap with 429 Too Many Requests
// (#4007: a count/byte window is a rate limit, not a too-large file; 413 stays for one oversized
// file and for the store cap). Retry-After (whole seconds) is set when the reset time is known.
func artifactHourlyLimited(w http.ResponseWriter, retryIn time.Duration, msg string) {
	secs := 0
	if retryIn > 0 {
		secs = int((retryIn + time.Second - 1) / time.Second)
	}
	// A per-action limit (X-Coral-Limit: action), so a dashboard never takes it for an overloaded server.
	httputil.WriteActionLimitBody(w, secs, map[string]string{"error": msg, "code": "artifact_quota", "message": msg, "limit": "hourly"})
}

// humanBytes shows a size rounded down (so a store just under its cap never reads as full):
// bytes under 1 KiB, else KiB, MiB or GiB to one decimal.
func humanBytes(n int64) string {
	unit := func(div float64, name string) string {
		return fmt.Sprintf("%.1f %s", math.Floor(float64(n)/div*10)/10, name)
	}
	switch {
	case n >= 1<<30:
		return unit(1<<30, "GiB")
	case n >= 1<<20:
		return unit(1<<20, "MiB")
	case n >= 1<<10:
		return unit(1<<10, "KiB")
	}
	return fmt.Sprintf("%d B", n)
}

// deleteArtifactFiles removes an artifact's object, its sidecar and every upload record of it,
// under its digest lock, and keeps the store's size current (#3553). It's the one delete path:
// the Operator's DELETE and #3554's prune both use it. found is false when neither the object
// nor its sidecar existed.
func deleteArtifactFiles(coralDir, id string) (freed int64, records int, found bool, err error) {
	if !objectNameRe.MatchString(id) {
		return 0, 0, false, nil
	}
	defer lockArtifact(id)()
	return deleteArtifactFilesLocked(coralDir, id)
}

// deleteArtifactFilesLocked is deleteArtifactFiles for a caller that already holds id's lock
// (#3554's prune re-checks a candidate under the lock, then deletes it).
func deleteArtifactFilesLocked(coralDir, id string) (freed int64, records int, found bool, err error) {
	objectPath := filepath.Join(coralDir, "artifacts", "objects", id)
	info, statErr := os.Stat(objectPath)
	_, sideErr := os.Stat(objectPath + ".json")
	if statErr != nil && sideErr != nil {
		return 0, 0, false, nil
	}
	if statErr == nil {
		if err := os.Remove(objectPath); err != nil {
			return 0, 0, true, err
		}
		freed = info.Size()
		storeFor(coralDir).removed(freed)
	}
	_ = os.Remove(objectPath + ".json")
	if sessions, rerr := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads")); rerr == nil {
		for _, s := range sessions {
			if up := uploadsDir(coralDir, s.Name()); up != "" && s.IsDir() {
				if os.Remove(filepath.Join(up, id+".json")) == nil {
					records++
				}
			}
		}
	}
	// And its pending receipts (#4440): a file waiting for a message that was never listed.
	for _, path := range pendingRecordPaths(coralDir, id) {
		if os.Remove(path) == nil {
			records++
		}
	}
	// Every board's shared and received rows of it too (#4379): the Operator's delete removes the
	// object and every row that lists it.
	for _, kind := range crossTeamRecordKinds {
		boards, rerr := os.ReadDir(filepath.Join(coralDir, "artifacts", kind))
		if rerr != nil {
			continue
		}
		for _, b := range boards {
			if b.IsDir() && boardDirName.MatchString(b.Name()) && os.Remove(filepath.Join(coralDir, "artifacts", kind, b.Name(), id+".json")) == nil {
				records++
			}
		}
	}
	return freed, records, true, nil
}

// deleteAgentArtifact: DELETE /api/artifacts/{id}, the Operator removes an artifact (#3553,
// from the #3551 audit): its object, its sidecar and every agent's upload record of it. A
// person only: the dashboard on this computer or the operator's signed command line, in every
// mode; an agent's token, a script, an API key and Coral Hub get 403 (and the tunnel never
// forwards it). Links to it in posts and task results stay and show "unavailable".
func (h *SessionsHandler) deleteAgentArtifact(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) || !humanInputCaller(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the Operator can delete an artifact, from this computer's dashboard or the signed command line", "code": "operator_only"})
		return
	}
	id := chi.URLParam(r, "id")
	if !objectNameRe.MatchString(id) {
		errBadRequest(w, "invalid artifact id")
		return
	}
	freed, removed, found, err := deleteArtifactFiles(h.cfg.CoralDir(), id)
	if !found {
		errNotFound(w, "artifact not found")
		return
	}
	if err != nil {
		errInternalServer(w, "could not delete the artifact")
		return
	}
	if h.db != nil { // an explicit delete also drops the Operator's keep mark (#3554)
		_, _ = h.db.ExecContext(r.Context(), "DELETE FROM artifact_keep WHERE hex = ?", id)
	}
	slog.Info("[artifacts] deleted by the Operator", "digest", id, "bytes", freed, "upload_records", removed)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "bytes": freed})
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
	// The object is opened once and served from that handle, its length from the file itself
	// (Security's #4438 INFO 4): a sidecar that disagrees with the file means the store is corrupt,
	// and the download is refused rather than sent with a length that doesn't match its bytes.
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		errNotFound(w, "artifact not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		errNotFound(w, "artifact not found")
		return
	}
	if info.Size() != meta.Size {
		slog.Warn("[artifacts] stored object and its sidecar disagree on the size; not served", "digest", id, "file_bytes", info.Size(), "sidecar_bytes", meta.Size)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "this artifact's stored copy is damaged", "code": "artifact_corrupt"})
		return
	}
	// An agent chose these bytes and their type: an HTML or SVG upload must not
	// run on the dashboard's origin when someone opens its link.
	setUntrustedContentHeaders(w.Header(), meta.MediaType, meta.Name)
	// The bytes never change for an id, but an artifact can be deleted (#3553): private, so no
	// shared cache (in front of Coral Hub, a proxy) keeps serving it after that.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", info.ModTime(), f) // Content-Length (and ranges) from the open file
}
