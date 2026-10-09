package routes

import (
	"bufio"
	"bytes"
	"context"
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
	if hours > 336 {
		hours = 336
	}
	rows, err := h.db.CallMetricSummary(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "rows": rows})
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
			// Browser requests commonly carry only the session ID. Resolve it to
			// the agent display name before falling back to route parameters;
			// otherwise many rows are grouped under a generic session/board name.
			if identity.sessionID != "" {
				var name string
				if err := db.GetContext(r.Context(), &name, `SELECT COALESCE(NULLIF(display_name, ''), agent_name) FROM live_sessions WHERE session_id = ?`, identity.sessionID); err == nil {
					identity.agentName = name
				}
			}
			if identity.agentName == "" {
				identity.agentName = firstNonEmpty(chi.URLParam(r, "name"), chi.URLParam(r, "sessionID"))
			}
			pattern := r.URL.Path
			if route := chi.RouteContext(r.Context()).RoutePattern(); route != "" {
				pattern = route
			}
			callType := "api"
			if isProxy {
				callType = "proxy"
			}
			metric := &store.CallMetric{
				CallType: callType, Operation: pattern,
				AgentName: identity.agentName, SessionID: identity.sessionID,
				BoardName: urlParam(r, "project"), Method: r.Method,
				StatusCode: rw.statusCode(), DurationMs: time.Since(started).Milliseconds(),
				IsError: rw.statusCode() >= http.StatusInternalServerError,
			}
			// Request logging must not change the response or turn a successful API
			// call into an error if the metrics database is unavailable.
			_ = db.RecordCallMetric(context.Background(), metric)
		})
	}
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
