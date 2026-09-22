package routes

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// decodeJSON decodes JSON from the request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func errBadRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func errNotFound(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": msg})
}

func errInternalServer(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
}

func errForbidden(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": msg})
}

func errConflict(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIf(cond bool, s string) any {
	if cond || s == "" {
		return nil
	}
	return s
}

// querySessionID extracts session_id from the query string and returns a
// *string (nil when absent/empty), ready for store methods that accept *string.
func querySessionID(r *http.Request) *string {
	return strPtr(r.URL.Query().Get("session_id"))
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefStrPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intPtrOr returns the value of p if non-nil, otherwise the default.
func intPtrOr(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

// promptOverrides extracts the default prompt override settings from a user
// settings map. Used by restart, resume, launch, and rejoin handlers.
func promptOverrides(settings map[string]string) map[string]string {
	return map[string]string{
		"default_prompt_orchestrator": settings["default_prompt_orchestrator"],
		"default_prompt_worker":       settings["default_prompt_worker"],
	}
}

// isProxyEnabledForAgent checks per-agent-type proxy settings.
// Codex uses "proxy_enabled_codex" (default: false).
// Claude and others use "proxy_enabled_claude", falling back to "proxy_enabled".
func isProxyEnabledForAgent(settings map[string]string, agentType string) bool {
	if agentType == "codex" {
		return strings.EqualFold(settings["proxy_enabled_codex"], "true")
	}
	if v, ok := settings["proxy_enabled_claude"]; ok {
		return strings.EqualFold(v, "true")
	}
	return strings.EqualFold(settings["proxy_enabled"], "true")
}

// emptyIfNil returns s unchanged when non-nil, or an empty slice of the same
// type when nil. This ensures JSON encodes as [] instead of null.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// isPathWithinDir checks that fullPath is within workdir after resolving symlinks.
// Returns true if the path is safe, false if it escapes the working directory.
func isPathWithinDir(workdir, fullPath string) bool {
	realWorkdir, _ := filepath.EvalSymlinks(workdir)
	realPath, _ := filepath.EvalSymlinks(fullPath)
	if realPath == "" {
		return true // file doesn't exist yet; caller handles separately if needed
	}
	return strings.HasPrefix(realPath, realWorkdir+string(os.PathSeparator))
}

// debugEnabled returns true when CORAL_DEBUG=1 is set.
var debugEnabled = sync.OnceValue(func() bool {
	return os.Getenv("CORAL_DEBUG") == "1"
})

// DebugRequestLogger returns middleware that logs session-related API calls
// when CORAL_DEBUG=1 is set. Logs method, path, and query params for
// /api/sessions/ and /ws/ endpoints.
// slowRequestThreshold is where an API call stops being a delay and starts
// being a symptom worth a log line. A variable so tests need not be slow.
var slowRequestThreshold = time.Second

func DebugRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if debugEnabled() {
			if strings.HasPrefix(path, "/api/sessions/") || strings.HasPrefix(path, "/ws/") {
				slog.Info("[debug] request", "method", r.Method, "path", path, "query", r.URL.RawQuery, "remote", r.RemoteAddr)
			}
		}
		// A slow API call is what makes the UI seem stuck, and it is invisible
		// after the fact. Websocket paths are long-lived by design.
		start := time.Now()
		next.ServeHTTP(w, r)
		if d := time.Since(start); d >= slowRequestThreshold && strings.HasPrefix(path, "/api/") {
			slog.Warn("slow request", "method", r.Method, "path", path, "query", r.URL.RawQuery, "duration_ms", d.Milliseconds())
		}
	})
}
