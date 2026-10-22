package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/subgentic/coral/internal/diskguard"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/safefs"
	setreg "github.com/subgentic/coral/internal/settings"
)

// urlParam extracts a chi URL parameter and percent-decodes it.
// chi uses RawPath when set, so characters like ' and ! that
// encodeURIComponent leaves alone arrive still-escaped; this
// ensures the handler always sees the decoded value.
func urlParam(r *http.Request, key string) string {
	v := chi.URLParam(r, key)
	if decoded, err := url.PathUnescape(v); err == nil {
		return decoded
	}
	return v
}

// routedPath is the path chi routes r on: the raw path when the request has one (chi's own rule), else the decoded one. The route lookups the gates make
// before routing must use it: urlParam decodes a parameter after chi has matched it, so "/api/sessions/live/a%2Fb/display-name" runs the handler of
// "/api/sessions/live/{name}/display-name" with the name "a/b", while the decoded path has an extra segment and matches no route (#4964).
func routedPath(r *http.Request) string {
	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}
	return r.URL.Path
}

// boardRoute splits /api/board/{project}/{tail} the way chi and urlParam read it: on the routed path (an encoded slash stays inside the project),
// then the project decoded. The tail is left as routed, so a comparison with a literal ("messages") means what the router means by it. ok is false
// for a path that is not under /api/board/ (#4977).
func boardRoute(r *http.Request) (project, tail string, ok bool) {
	rest, ok := strings.CutPrefix(routedPath(r), "/api/board/")
	if !ok {
		return "", "", false
	}
	project, tail, _ = strings.Cut(rest, "/")
	if d, err := url.PathUnescape(project); err == nil {
		project = d
	}
	return project, tail, true
}

// decodeJSON decodes JSON from the request body into v. Every body is already capped by BodyLimit (body_limit.go); a handler that takes less than that says so with decodeJSONLimit or
// decodeJSONMax, which differ only in how the cut shows: a read of at most n bytes (the rest is never read, a longer document fails to decode), or MaxBytesReader (the reader errors with
// *http.MaxBytesError and the server closes the connection after the reply).
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// decodeJSONLimit is decodeJSON on the first n bytes of the body.
func decodeJSONLimit(r *http.Request, v any, n int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, n)).Decode(v)
}

// decodeJSONMax is decodeJSON on a body capped at n by http.MaxBytesReader.
func decodeJSONMax(w http.ResponseWriter, r *http.Request, v any, n int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, n)).Decode(v)
}

// writeError answers status with the error envelope every route uses: {"error": msg}, and "code" next to it when the client is meant to match on it (#5262). The err* helpers below are
// this with the status in their name. Extra fields (a limit, the field at fault) are not part of the envelope: such a reply is written with writeJSON.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	if code == "" {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func errBadRequest(w http.ResponseWriter, msg string) { writeError(w, http.StatusBadRequest, "", msg) }

// errTaskStore answers a failed task operation: 404 "Task not found" when there is no such task (the database's own "sql: no rows in result set", or the
// "task #N is not on this board" of the merge queue), else 400 with the error's words.
func errTaskStore(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || taskNotOnBoard(err) {
		errNotFound(w, "Task not found")
		return
	}
	errBadRequest(w, err.Error())
}

// taskNotOnBoard is the merge queue's "task #N is not on this board" (a reviewer who is not on the board has "<name> is not on this board", which is not this).
func taskNotOnBoard(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "task #") && strings.Contains(msg, " is not on this board")
}

// errTaskGone is errTaskStore for the routes whose store can also fail on another lookup (a reviewer who is not a subscriber): only the explicit
// "task #N is not on this board" is a 404.
func errTaskGone(w http.ResponseWriter, err error) {
	if taskNotOnBoard(err) {
		errNotFound(w, "Task not found")
		return
	}
	errBadRequest(w, err.Error())
}

func errNotFound(w http.ResponseWriter, msg string) { writeError(w, http.StatusNotFound, "", msg) }

func errInternalServer(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusInternalServerError, "", msg)
}

func errForbidden(w http.ResponseWriter, msg string) { writeError(w, http.StatusForbidden, "", msg) }

func errPayloadTooLarge(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusRequestEntityTooLarge, "", msg)
}

func errConflict(w http.ResponseWriter, msg string) { writeError(w, http.StatusConflict, "", msg) }

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

// clampLimit bounds a list size a caller asked for (#3620, hub audit H3): a missing, junk, zero or
// negative value is the route's default (a negative SQLite LIMIT means no limit at all), and
// anything above the route's maximum is the maximum. Every list route a viewer can call, locally
// or through Coral Hub, goes through it.
func clampLimit(n, def, max int) int {
	if n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// clampOffset keeps a list offset at zero or above (a negative OFFSET is an SQLite error or, for
// some queries, a full scan).
func clampOffset(n int) int {
	if n < 0 {
		return 0
	}
	return n
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
	return ptr.NonEmpty(r.URL.Query().Get("session_id"))
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
		"default_prompt_orchestrator": setreg.Raw(settings, "default_prompt_orchestrator"),
		"default_prompt_worker":       setreg.Raw(settings, "default_prompt_worker"),
	}
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
// For READS only: a path that doesn't exist yet counts as inside without looking at where its
// existing parent really is (a junction or symlinked folder could lead out). Never use it before
// creating or writing a file; use safefs.Within (it resolves the deepest existing ancestor) or
// write through a safefs root (#3633, Security's NIT).
func isPathWithinDir(workdir, fullPath string) bool {
	realWorkdir, _ := safefs.Resolve(workdir)
	if realWorkdir == "" {
		return false // an unresolvable root contains nothing (the old prefix test passed everything)
	}
	realFull, err := safefs.Resolve(fullPath)
	if err != nil {
		// A file that doesn't exist yet: the caller handles it. Any other failure to resolve
		// is outside: a path that can't be resolved is never taken as inside (#3633).
		return errors.Is(err, fs.ErrNotExist)
	}
	return strings.HasPrefix(realFull, realWorkdir+string(os.PathSeparator))
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

// credentialQueryName reports whether a query parameter name carries a credential: the known
// names, and any name ending in _token, _key or _secret, so a dashboard sign-in link's
// login_token (a live session credential for 10 minutes) never reaches coral.log (#3970,
// Security's #3967 finding 3b).
func credentialQueryName(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "api_key", "apikey", "key", "token", "access_token", "authorization", "secret", "password", "login_token":
		return true
	}
	return strings.HasSuffix(n, "_token") || strings.HasSuffix(n, "_key") || strings.HasSuffix(n, "_secret")
}

// hasCredentialQuery reports whether r carries a credential in its query string
// (an API key, a token). It reads no value: handlers use it to refuse such a
// request, so it must not register as a query parameter the route accepts
// (pkg/tunnel/routes_query.go, #3279: through the Coral Hub tunnel a secret in a
// URL would end up in logs, and nothing on this server needs one there).
func hasCredentialQuery(r *http.Request) bool {
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return true // unparseable: treat as carrying one
	}
	for k := range vals {
		if credentialQueryName(k) {
			return true
		}
	}
	return false
}

// RedactQuery masks credential-bearing query parameters (api_key and friends)
// so a raw query string can be logged safely.
func RedactQuery(raw string) string {
	if raw == "" {
		return raw
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return "[unparseable query redacted]"
	}
	changed := false
	for k := range vals {
		if credentialQueryName(k) {
			for i := range vals[k] {
				vals[k][i] = "REDACTED"
			}
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return vals.Encode()
}

func DebugRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if debugEnabled() {
			if strings.HasPrefix(path, "/api/sessions/") || strings.HasPrefix(path, "/ws/") {
				slog.Info("[debug] request", "method", r.Method, "path", path, "query", RedactQuery(r.URL.RawQuery), "remote", r.RemoteAddr)
			}
		}
		// A slow API call is what makes the UI seem stuck, and it is invisible
		// after the fact. Websocket paths are long-lived by design.
		start := time.Now()
		next.ServeHTTP(w, r)
		if d := time.Since(start); d >= slowRequestThreshold && strings.HasPrefix(path, "/api/") {
			slog.Warn("slow request", "method", r.Method, "path", path, "query", RedactQuery(r.URL.RawQuery), "duration_ms", d.Milliseconds())
		}
	})
}

// The sizes a terminal may be asked to take (#4602). The Operator, or a Hub user at Interact, could ask for 65535 columns (the
// websocket path wrapped through uint16), and every viewer's replay and screen read then grows with it.
const (
	minTerminalCols, maxTerminalCols = 10, 1000
	maxTerminalRows                  = 300
	defaultTerminalRows              = 50
)

// clampTerminalSize is a requested cols x rows as a size the backend takes: columns at most maxTerminalCols, rows from 1 to
// maxTerminalRows (none or a negative value is the default of 50, never a wrapped uint16).
func clampTerminalSize(cols, rows int) (uint16, uint16) {
	if rows < 1 {
		rows = defaultTerminalRows
	}
	return uint16(max(minTerminalCols, min(cols, maxTerminalCols))), uint16(min(rows, maxTerminalRows))
}

// refuseIfDiskLow answers 507 disk_low and reports true when the volume Coral stores its data on is nearly full (under 1 GiB or 1%
// free, internal/diskguard): the routes that can fill it (agent artifact uploads, board posts, a cross-team file) refuse before a
// write fails halfway, since a full disk makes SQLite fail for every agent at once (#4602). Events and the Operator's settings are not
// refused: they are small, and refusing them would hide the state of the agents that should show the problem.
func refuseIfDiskLow(w http.ResponseWriter) bool {
	low, free := diskguard.Low()
	if !low {
		return false
	}
	const msg = "The disk Coral stores its data on is almost full, so this was not saved. Free some space (Settings > Storage shows what Coral uses) and try again."
	writeJSON(w, http.StatusInsufficientStorage, map[string]any{
		"error":      msg,
		"message":    msg, // coral-board prints "message" before the code (serverError)
		"code":       "disk_low",
		"free_bytes": free,
		"not_sent":   true,
	})
	return true
}

// getByID answers GET of one row named by a numeric URL parameter, the way GetView, GetWorkflow and
// GetWorkflowRun each did: 400 with badID when the parameter is not a number, 500 for a store
// failure, 404 with missing when the store has no such row, else the row as JSON.
func getByID[T any](w http.ResponseWriter, r *http.Request, param, badID, missing string, get func(ctx context.Context, id int64) (*T, error)) {
	id, err := strconv.ParseInt(chi.URLParam(r, param), 10, 64)
	if err != nil {
		errBadRequest(w, badID)
		return
	}
	row, err := get(r.Context(), id)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if row == nil {
		errNotFound(w, missing)
		return
	}
	writeJSON(w, http.StatusOK, row)
}
