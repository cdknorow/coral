package routes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
)

type CallMetricsHandler struct{ db *store.DB }

func NewCallMetricsHandler(db *store.DB) *CallMetricsHandler { return &CallMetricsHandler{db: db} }

// Summary returns grouped tool/API call counts for the requested window.
func (h *CallMetricsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	// Never more than the raw rows kept (#3261): a window longer than the
	// retention would be reported as complete while missing its older part.
	maxHours := 336
	if settings, err := store.NewSessionStore(h.db).GetSettings(r.Context()); err == nil {
		if keep := store.CallMetricsRetentionDays(settings) * 24; keep < maxHours {
			maxHours = keep
		}
	}
	if hours > maxHours {
		hours = maxHours
	}
	rows, err := h.db.CallMetricSummary(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// cap_dropped: metrics not stored since Coral started because the raw rows were at their cap (#5359), so a gap after a burst is visible, not silent (#5368).
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "rows": rows, "cap_dropped": h.db.CallMetricsCapDropped()})
}

func (h *CallMetricsHandler) Record(w http.ResponseWriter, r *http.Request) {
	var metric store.CallMetric
	if err := decodeJSON(r, &metric); err != nil || metric.CallType == "" || metric.Operation == "" {
		errBadRequest(w, "call_type and operation are required")
		return
	}
	if err := h.db.RecordCallMetric(r.Context(), &metric); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, metric)
}

// RequestMetrics records API and LLM proxy traffic at the server boundary.
// This keeps instrumentation independent of every CLI and browser client.
// The metrics endpoints themselves are excluded to avoid recursively counting
// the logger.
func RequestMetrics(db *store.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isAPI := strings.HasPrefix(r.URL.Path, "/api/")
			isProxy := strings.HasPrefix(r.URL.Path, "/proxy/")
			if db == nil || (!isAPI && !isProxy) || strings.HasPrefix(r.URL.Path, "/api/call-metrics") {
				next.ServeHTTP(w, r)
				return
			}
			identity := requestMetricIdentity(r)
			started := time.Now()
			rw := &metricResponseWriter{ResponseWriter: w}
			next.ServeHTTP(rw, r)
			pattern := r.URL.Path
			if route := chi.RouteContext(r.Context()).RoutePattern(); route != "" {
				pattern = route
			}
			if store.IsInternalMetricOperation(pattern) {
				return // dashboard and hub plumbing, not a tool or API call (#3152)
			}
			if r.Method == http.MethodGet && dashboardPollOperations[pattern] && identity.agentName == "" {
				return // the dashboard's own refresh polls (#3261)
			}
			// Through the hub's proxy, skip only the remote's polls; proxied
			// actions (launch, send, …) count like local ones.
			if pattern == "/api/remote/{server}/*" && store.IsInternalRemotePath(r.Method, "/"+chi.URLParam(r, "*")) {
				return
			}
			callType := "api"
			if isProxy {
				callType = "proxy"
			}
			// Browser requests commonly carry only the session ID. The queued
			// metric resolves it to the agent display name when the batch is
			// written, falling back to route parameters; otherwise many rows are
			// grouped under a generic session/board name.
			metric := &store.CallMetric{
				CallType: callType, Operation: pattern,
				AgentName: identity.agentName, SessionID: identity.sessionID,
				FallbackAgentName: firstNonEmpty(chi.URLParam(r, "name"), chi.URLParam(r, "sessionID")),
				BoardName:         urlParam(r, "project"), Method: r.Method,
				StatusCode: rw.statusCode(), DurationMs: time.Since(started).Milliseconds(),
				IsError: rw.statusCode() >= http.StatusInternalServerError,
			}
			// Request logging must not change the response, slow it down, or turn
			// a successful API call into an error if the metrics database is
			// unavailable, so metrics are written asynchronously in batches.
			db.EnqueueCallMetric(metric)
		})
	}
}

// dashboardPollOperations are the routes the dashboard polls on its own while
// an agent is open (chat, poll, events, the board's message
// list). A GET from the dashboard carries no agent identity and is not a call
// anyone made; the same routes called by an agent's CLI (X-Coral-Agent,
// X-Coral-Subscriber-ID or subscriber_id) still count. pending-tool is kept: the
// PreToolUse hook posts it once per tool call, which is the tool-call signal
// (#3261, the operator's choice).
var dashboardPollOperations = map[string]bool{
	"/api/sessions/live/{name}/chat":    true,
	"/api/sessions/live/{name}/poll":    true,
	"/api/sessions/live/{name}/events":  true,
	"/api/board/{project}/messages/all": true,
}

type requestMetricIdentityData struct {
	agentName string
	sessionID string
}

func requestMetricIdentity(r *http.Request) requestMetricIdentityData {
	identity := requestMetricIdentityData{}
	identity.agentName = firstNonEmpty(r.Header.Get("X-Coral-Agent"), r.Header.Get("X-Coral-Subscriber-ID"), r.URL.Query().Get("subscriber_id"))
	identity.sessionID = firstNonEmpty(r.Header.Get("X-Coral-Session-ID"), r.URL.Query().Get("session_id"))
	// Most CLI mutations carry subscriber_id in a small JSON body. Read and
	// restore it so handlers receive the exact original request stream.
	if identity.agentName == "" && strings.Contains(r.Header.Get("Content-Type"), "application/json") && (r.ContentLength <= 65536 || r.ContentLength < 0) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			var fields struct {
				SubscriberID string `json:"subscriber_id"`
				SessionID    string `json:"session_id"`
			}
			if json.Unmarshal(body, &fields) == nil {
				identity.agentName = fields.SubscriberID
				if identity.sessionID == "" {
					identity.sessionID = fields.SessionID
				}
			}
		}
	}
	return identity
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type metricResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *metricResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps streaming (SSE) responses streaming through the metrics wrapper.
func (w *metricResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack keeps WebSocket upgrades working on /api/ routes (for example the
// hub's /api/remote/{server}/ws/... bridge); without it websocket.Accept fails
// with 501 because the wrapper hid the underlying http.Hijacker.
func (w *metricResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (w *metricResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
