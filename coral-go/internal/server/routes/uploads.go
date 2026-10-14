package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/uploads"
)

var (
	// uploadDir is set by InitUploadDir() from server startup.
	// Falls back to ~/.coral/uploads if not initialized.
	uploadDir = ""

	allowedExtensions = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".bmp": true, ".tiff": true,
		// SVG intentionally excluded: SVG files can contain embedded JavaScript,
		// creating a stored XSS risk if served inline.
	}

	contentTypeToExt = map[string]string{
		"image/png":  ".png",
		"image/jpeg": ".jpg",
		"image/gif":  ".gif",
		"image/webp": ".webp",
		"image/bmp":  ".bmp",
		// "image/svg+xml" excluded: XSS risk
		"image/tiff": ".tiff",
	}

	maxFileSize int64 = 20 * 1024 * 1024 // 20 MB
)

// uploadSettings reads the user settings for the uploads cap (#3364); nil uses the
// defaults.
var uploadSettings func(context.Context) (map[string]string, error)

// SetUploadSettings gives the upload handler the settings reader.
func SetUploadSettings(fn func(context.Context) (map[string]string, error)) { uploadSettings = fn }

// InitUploadDir sets the upload directory based on the coral data directory.
// Must be called during server initialization before handling upload requests.
func InitUploadDir(coralDir string) {
	uploadDir = filepath.Join(coralDir, "uploads")
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

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
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
			writeJSON(w, http.StatusInsufficientStorage, map[string]any{"error": fmt.Sprintf(
				"The uploads folder is full (%d MB, uploads_max_mb): uploads from the last hour are kept. Try again later, or raise the limit.",
				uploads.MaxBytes(settings)>>20)})
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

	dest := filepath.Join(uploadDir, safeName)
	if err := os.WriteFile(dest, content, 0644); err != nil {
		errInternalServer(w, "Failed to save file")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"path":     filepath.ToSlash(dest),
		"filename": orDefault(filename, safeName),
		"size":     len(content),
	})
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
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such upload"})
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
