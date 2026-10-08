package routes

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
		"image/png":     ".png",
		"image/jpeg":    ".jpg",
		"image/gif":     ".gif",
		"image/webp":    ".webp",
		"image/bmp":     ".bmp",
		// "image/svg+xml" excluded: XSS risk
		"image/tiff":    ".tiff",
	}

	maxFileSize int64 = 20 * 1024 * 1024 // 20 MB
)

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
