package routes

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
)

// Request-body limits (#3998, Security's #3994 finding 1): before, no route capped what it read,
// so one large POST (an agent's hook, a buggy script, a Hub request) could take the server's
// memory, and every pty agent with it. Every request body is now capped: defaultBodyLimit, or the
// route's own entry below when it legitimately takes more. Routes with a tighter cap of their own
// (the board, agent UI panels, remote servers) keep it.
const defaultBodyLimit = 8 << 20

// bodyLimits are the routes allowed a larger body than defaultBodyLimit, by "METHOD /pattern".
// TestBodyLimitTableNamesServedRoutes keeps every entry a served route.
var bodyLimits = map[string]int64{
	"POST /api/upload":                           maxFileSize + 1<<20,      // a 20 MB image plus multipart framing
	"POST /api/agent/artifacts":                  maxAgentArtifactSize + 1, // the artifact's own limit
	"PUT /api/sessions/live/{name}/file-content": 50 << 20,                 // the editor's save (SaveFileContent's own cap)
	// A file another Coral sends through Coral Hub: the frame is a 4-byte length, the metadata and the file
	// (the handler refuses a declared length over this before it reads a byte, #4453).
	"POST /api/board/{project}/external-artifacts": 4 + maxReceivedMeta + maxReceivedArtifact,
}

// BodyLimitKeys lists the routes with a raised limit (tests).
func BodyLimitKeys() []string {
	keys := make([]string, 0, len(bodyLimits))
	for k := range bodyLimits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bodyLimitFor is r's limit: its route's entry, else the default.
func bodyLimitFor(r *http.Request) int64 {
	if len(bodyLimits) > 0 {
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.Routes != nil {
			tctx := chi.NewRouteContext()
			if rc.Routes.Match(tctx, r.Method, routedPath(r)) {
				if n, ok := bodyLimits[r.Method+" "+tctx.RoutePattern()]; ok {
					return n
				}
			}
		}
	}
	return defaultBodyLimit
}

// BodyLimit caps every request body. A declared Content-Length over the limit is answered 413
// at once, before anything is read. A body without one (chunked) is cut at the limit, so it can
// never take more memory than that; a handler that then fails to decode it answers 400, which
// is sent as 413 (decodeJSON and the other readers need no change).
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		limit := bodyLimitFor(r)
		if r.ContentLength > limit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "the request body is too large", "code": "body_too_large", "limit": limit})
			return
		}
		lb := &limitedBody{ReadCloser: http.MaxBytesReader(w, r.Body, limit)}
		r.Body = lb
		next.ServeHTTP(&tooLargeWriter{ResponseWriter: w, body: lb}, r)
	})
}

// limitedBody remembers that the limit was hit.
type limitedBody struct {
	io.ReadCloser
	hit atomic.Bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		b.hit.Store(true)
	}
	return n, err
}

// tooLargeWriter turns a handler's 400 into 413 when the body was cut at the limit.
type tooLargeWriter struct {
	http.ResponseWriter
	body *limitedBody
}

func (t *tooLargeWriter) WriteHeader(code int) {
	if code == http.StatusBadRequest && t.body.hit.Load() {
		code = http.StatusRequestEntityTooLarge
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *tooLargeWriter) Write(b []byte) (int, error) { return t.ResponseWriter.Write(b) }

// Unwrap lets http.ResponseController reach the flusher and hijacker underneath.
func (t *tooLargeWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// Flush passes a flush through (a streamed answer to a request with a body).
func (t *tooLargeWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
