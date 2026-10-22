package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/uploads"
	"github.com/subgentic/coral/pkg/tunnel"
)

var (
	// uploadDir is set by InitUploadDir() from server startup.
	// Falls back to ~/.coral/uploads if not initialized.
	uploadDir = ""
	// uploadCoralDir is the data folder the artifact copy of an image goes to (#5284).
	uploadCoralDir = ""

	allowedExtensions = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".bmp": true, ".tiff": true,
		// SVG (#5284): accepted as a file for the agent only, never as an artifact and never served by GET /api/upload/{name} (thumbnailTypes), so it is not inline markup on the
		// dashboard's origin and not shared with Coral Hub viewers (a byte-exact copy cannot be metadata-stripped and can carry script).
		".svg": true,
	}

	contentTypeToExt = map[string]string{
		"image/png":     ".png",
		"image/jpeg":    ".jpg",
		"image/gif":     ".gif",
		"image/webp":    ".webp",
		"image/bmp":     ".bmp",
		"image/tiff":    ".tiff",
		"image/svg+xml": ".svg", // (#5284), see allowedExtensions
	}

	maxFileSize int64 = 20 * 1024 * 1024 // 20 MB

	// An iPhone's own photo format. Coral keeps images the agent can read; the picker converts a library photo to JPEG, so a HEIC that arrives was sent as the original (#6088).
	heicExtensions = map[string]bool{".heic": true, ".heif": true, ".heics": true, ".avif": true}
)

// heicRefusal is said when a HEIC (or AVIF) image is sent: a sentence the person can act on, not a list of allowed extensions.
const heicRefusal = "Coral can't read HEIC photos. Send it as a JPEG or PNG (on an iPhone: Settings > Camera > Formats > Most Compatible, or pick the photo again from Photos)."

func headerContentType(h *multipart.FileHeader) string {
	if h == nil {
		return ""
	}
	return h.Header.Get("Content-Type")
}

// uploadSettings reads the user settings for the uploads cap (#3364); nil uses the
// defaults.
var uploadSettings func(context.Context) (map[string]string, error)

// SetUploadSettings gives the upload handler the settings reader.
func SetUploadSettings(fn func(context.Context) (map[string]string, error)) { uploadSettings = fn }

// InitUploadDir sets the upload directory based on the coral data directory.
// Must be called during server initialization before handling upload requests.
func InitUploadDir(coralDir string) {
	uploadDir = filepath.Join(coralDir, "uploads")
	uploadCoralDir = coralDir
}

// UploadFile handles POST /api/upload — upload an image and return its path.
func UploadFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxFileSize); err != nil {
		errBadRequest(w, "File too large or invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		errBadRequest(w, "No file provided")
		return
	}
	defer file.Close()

	filename := ""
	if header != nil {
		filename = header.Filename
	}

	ext := ""
	if filename != "" {
		ext = strings.ToLower(filepath.Ext(filename))
	}

	// Fall back to content type for clipboard pastes
	if ext == "" {
		ct := ""
		if header != nil {
			ct = header.Header.Get("Content-Type")
		}
		if ct != "" {
			ext = contentTypeToExt[ct]
		}
	}

	if heicExtensions[ext] || strings.Contains(strings.ToLower(headerContentType(header)), "heic") || strings.Contains(strings.ToLower(headerContentType(header)), "heif") {
		errBadRequest(w, heicRefusal)
		return
	}
	if ext == "" || !allowedExtensions[ext] {
		allowed := make([]string, 0, len(allowedExtensions))
		for k := range allowedExtensions {
			allowed = append(allowed, k)
		}
		errBadRequest(w, fmt.Sprintf("Unsupported file type: %s. Allowed: %s", ext, strings.Join(allowed, ", ")))
		return
	}

	content, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		errInternalServer(w, "Failed to read file")
		return
	}
	if int64(len(content)) > maxFileSize {
		errBadRequest(w, fmt.Sprintf("File too large (%d bytes). Max: %d bytes", len(content), maxFileSize))
		return
	}

	if err := os.MkdirAll(uploadDir, 0o700); err != nil { // this user's files only (#3602 W28)
		errInternalServer(w, "Failed to create upload directory")
		return
	}

	// The folder's size cap (uploads_max_mb, #3364): the oldest uploads go first, never
	// one from the last hour.
	var settings map[string]string
	if uploadSettings != nil {
		settings, _ = uploadSettings(r.Context())
	}
	if err := uploads.MakeRoom(uploadDir, int64(len(content)), uploads.MaxBytes(settings), time.Now()); err != nil {
		if errors.Is(err, uploads.ErrFull) {
			writeError(w, http.StatusInsufficientStorage, "", fmt.Sprintf(
				"The uploads folder is full (%d MB, uploads_max_mb): uploads from the last hour are kept. Try again later, or raise the limit.",
				uploads.MaxBytes(settings)>>20))
			return
		}
		errInternalServer(w, "Failed to check the uploads folder")
		return
	}

	// Generate safe filename
	isClipboard := filename == "" || strings.EqualFold(filename, "image.png") || strings.EqualFold(filename, "blob")
	var safeName string
	if isClipboard {
		ts := time.Now().Format("20060102_150405")
		safeName = fmt.Sprintf("screenshot_%s_%s%s", ts, shortHex(4), ext)
	} else {
		safeName = fmt.Sprintf("%s_%s", shortHex(8), filepath.Base(filename))
	}
	safeName = strings.ReplaceAll(safeName, " ", "_")

	// A JPEG, PNG or GIF loses its metadata (a phone photo's GPS) before it is saved: the agent's file and the artifact are the same bytes (#5284).
	content = strippedUpload(content)
	dest := filepath.Join(uploadDir, safeName)
	if err := os.WriteFile(dest, content, 0o600); err != nil {
		errInternalServer(w, "Failed to save file")
		return
	}

	reply := map[string]any{
		"ok":       true,
		"path":     filepath.ToSlash(dest),
		"filename": orDefault(filename, safeName),
		"size":     len(content),
	}
	// The same image as an artifact (#5284), on the target agent's session record: the chat shows it from the artifact route, which Coral Hub serves. Whatever stops it is said in the reply;
	// the upload itself has succeeded and the agent has its path.
	sid := r.FormValue("session_id")
	if !safefs.IsSafeID(sid) {
		sid = ""
	}
	via := uploadVia{Hub: cloud.Tunneled(r)}
	if via.Hub {
		if u := r.Header.Get(tunnel.HubUserHeader); tunnel.ValidHubUser(u) { // the hub account (hub-user-<8 hex>), never a name or an email; the connector forwards it on this route only (#6088)
			via.User = u
		}
		slog.Info("image uploaded through Coral Hub", "hub_user", via.User, "bytes", len(content))
	}
	if art, why, retryAt := shareUploadedImage(r.Context(), uploadCoralDir, sid, orDefault(filename, safeName), ext, content, via); art != nil {
		reply["artifact"] = art
	} else {
		reply["artifact_error"] = why
		if !retryAt.IsZero() {
			reply["artifact_retry_at"] = retryAt.UTC().Format(time.RFC3339) // the dashboard says "try again at HH:MM" in the viewer's own time
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// thumbnailTypes are the image types GET /api/upload/{name} serves, by what the bytes are (net/http's sniff of the first 512 bytes), never by the
// name or the extension. SVG (text/xml or text/plain to the sniffer), BMP and TIFF are not here: the chip's thumbnail is a PNG, JPEG, GIF or WebP.
// maxUploadNameLen is the longest file name GET /api/upload/{name} looks up: file systems stop at 255 bytes, so a longer one names nothing.
const maxUploadNameLen = 255

var thumbnailTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// GetUpload serves one uploaded image to the dashboard's chat chips (#5028). GET /api/upload/{name}
//
// name is the file's base name, as the last element of the upload's path: no separator, no "..", no device name, never a
// symlink or a folder (safefs.Root resolves it inside the uploads folder and opens it without following a link). Anything that is
// not an upload by that rule is 400; a missing file is 404; any other failure to open it is refused, not served. The type is what
// the bytes are, from the allow-list above (415 otherwise); the reply is nosniff, inline with a plain file name, sandboxed, capped
// at the upload size, and cacheable by the browser that asked (the name is random, the file never changes).
func GetUpload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if len(name) > maxUploadNameLen*3 { // a name no upload has (the encoded form of one can be three times as long): refused before it is decoded or looked up
		errBadRequest(w, "not an upload name")
		return
	}
	if strings.Contains(name, "%") { // chi hands the segment as it came on the wire: a browser percent-encodes a name with a space-free but non-ASCII letter
		decoded, err := url.PathUnescape(name)
		if err != nil {
			errBadRequest(w, "not an upload name")
			return
		}
		name = decoded // once: the checks below run on the decoded name, so an encoded separator or dot segment is refused as the plain one is
	}
	if len(name) > maxUploadNameLen || name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") || safefs.CheckName(name) != nil {
		errBadRequest(w, "not an upload name")
		return
	}
	root, err := safefs.OpenRoot(uploadDir)
	if err != nil {
		if errors.Is(err, safefs.ErrRootGone) {
			errNotFound(w, "no such upload")
			return
		}
		errInternalServer(w, "Failed to open the uploads folder")
		return
	}
	defer root.Close()
	f, fi, err := root.Open(name)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			errNotFound(w, "no such upload")
		case errors.Is(err, safefs.ErrNotRegular), errors.Is(err, safefs.ErrReservedName):
			errBadRequest(w, "not an upload name")
		default:
			errInternalServer(w, "Failed to open the upload") // fail closed: nothing is served on an error that is not "not found"
		}
		return
	}
	defer f.Close()
	if fi.Size() > maxFileSize {
		errPayloadTooLarge(w, "the upload is too large to show")
		return
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		errInternalServer(w, "Failed to read the upload")
		return
	}
	ct := http.DetectContentType(head[:n])
	if !thumbnailTypes[ct] {
		writeError(w, http.StatusUnsupportedMediaType, "", "not a PNG, JPEG, GIF or WebP image")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		errInternalServer(w, "Failed to read the upload")
		return
	}
	setImageHeaders(w, ct, name, fi.Size())
	w.WriteHeader(http.StatusOK)
	io.Copy(w, io.LimitReader(f, fi.Size()))
}

// setImageHeaders is the hardening every image reply of the dashboard's thumbnails has (GET /api/upload/{name}, a chat's attachment): the sniffed type,
// nosniff, inline with a plain file name, a sandboxing CSP, a private cache and the length.
func setImageHeaders(w http.ResponseWriter, ct, name string, size int64) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": safeDownloadName(name)}))
	h.Set("Cache-Control", "private, no-cache") // the browser may keep it but asks again: an upload can be removed
	h.Set("Content-Length", fmt.Sprint(size))
}

// safeDownloadName keeps letters, digits, '.', '_' and '-' of a file name: what goes into Content-Disposition is never a header or a path.
func safeDownloadName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}

// DeleteUpload removes one upload by its file name (#3364).
// DELETE /api/upload/{name}
func DeleteUpload(w http.ResponseWriter, r *http.Request) {
	err := uploads.Delete(uploadDir, chi.URLParam(r, "name"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, uploads.ErrBadName):
		errBadRequest(w, "not an upload name")
	case errors.Is(err, os.ErrNotExist):
		errNotFound(w, "no such upload")
	default:
		errInternalServer(w, "Failed to delete the upload")
	}
}

func shortHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
