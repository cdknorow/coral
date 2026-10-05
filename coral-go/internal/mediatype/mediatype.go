// Package mediatype infers artifact media types from file names without
// depending on the host's mime database (which lacks .md on some platforms).
package mediatype

import (
	"mime"
	"path/filepath"
	"strings"
)

const Generic = "application/octet-stream"

// portable lists types that must resolve identically on every platform.
var portable = map[string]string{
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
}

// IsGeneric reports whether a declared media type carries no information.
func IsGeneric(declared string) bool {
	t := strings.TrimSpace(declared)
	if t == "" {
		return true
	}
	if parsed, _, err := mime.ParseMediaType(t); err == nil {
		t = parsed
	}
	t = strings.ToLower(t)
	return t == "application/octet-stream" || t == "binary/octet-stream"
}

// FromName returns the media type implied by name's extension, or "" if the
// extension is unknown. Unknown extensions are never guessed.
func FromName(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == "" {
		return ""
	}
	if t, ok := portable[ext]; ok {
		return t
	}
	return mime.TypeByExtension(ext)
}

// Resolve keeps an explicit, specific declared type and otherwise recovers one
// from the first name that implies a type. It returns "" when nothing applies.
func Resolve(declared string, names ...string) string {
	if !IsGeneric(declared) {
		return strings.TrimSpace(declared)
	}
	for _, n := range names {
		if t := FromName(n); t != "" {
			return t
		}
	}
	return strings.TrimSpace(declared)
}

// EnsureExtension appends fallbackFrom's extension to name when name has no
// usable extension of its own, so custom display names keep their file type.
func EnsureExtension(name, fallbackFrom string) string {
	if hasExt(name) {
		return name
	}
	ext := filepath.Ext(strings.TrimSpace(fallbackFrom))
	if !hasExt("x" + ext) {
		return name
	}
	return name + ext
}

func hasExt(name string) bool {
	ext := filepath.Ext(strings.TrimSpace(name))
	if len(ext) < 2 || len(ext) > 11 {
		return false
	}
	for _, c := range ext[1:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
