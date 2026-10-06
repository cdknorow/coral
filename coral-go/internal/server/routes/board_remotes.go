package routes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
)

// BoardRemotesHandler handles remote board subscription and proxy endpoints.
type BoardRemotesHandler struct {
	rbs *store.RemoteBoardStore
	cfg *config.Config
}

// NewBoardRemotesHandler creates a new BoardRemotesHandler.
func NewBoardRemotesHandler(db *store.DB, cfg *config.Config) *BoardRemotesHandler {
	return &BoardRemotesHandler{
		rbs: store.NewRemoteBoardStore(db),
		cfg: cfg,
	}
}

// --- Subscription CRUD ---

type remoteSubRequest struct {
	SessionID    string `json:"session_id"`
	RemoteServer string `json:"remote_server"`
	Project      string `json:"project"`
	JobTitle     string `json:"job_title"`
}

type remoteSubDeleteRequest struct {
	SessionID string `json:"session_id"`
}

// AddSubscription handles POST /api/board/remotes
func (h *BoardRemotesHandler) AddSubscription(w http.ResponseWriter, r *http.Request) {
	var req remoteSubRequest
	if err := decodeJSON(r, &req); err != nil {
		errBadRequest(w, "invalid request body")
		return
	}

	// SSRF validation: ensure the remote server URL doesn't resolve to private/reserved IPs
	if _, err := httputil.ResolveAndValidateURL(req.RemoteServer); err != nil {
		errBadRequest(w, err.Error())
		return
	}

	sub, err := h.rbs.AddRemoteSub(r.Context(), req.SessionID, req.RemoteServer, req.Project, req.JobTitle)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// RemoveSubscription handles DELETE /api/board/remotes
func (h *BoardRemotesHandler) RemoveSubscription(w http.ResponseWriter, r *http.Request) {
	var req remoteSubDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		errBadRequest(w, "invalid request body")
		return
	}

	removed, err := h.rbs.RemoveRemoteSubs(r.Context(), req.SessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// ListSubscriptions handles GET /api/board/remotes
func (h *BoardRemotesHandler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.rbs.ListAllRemoteSubs(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, subs)
}

// --- Proxy Endpoints ---

// ProxyProjects handles GET /api/board/remotes/proxy/{remote_server}/projects
func (h *BoardRemotesHandler) ProxyProjects(w http.ResponseWriter, r *http.Request) {
	remoteServer := chi.URLParam(r, "*")
	// Extract the remote_server and path parts
	// URL pattern: /api/board/remotes/proxy/{remote_server}/projects
	// The wildcard captures everything after /proxy/
	parts := strings.SplitN(remoteServer, "/projects", 2)
	if len(parts) == 0 {
		errBadRequest(w, "missing remote server")
		return
	}
	server := parts[0]
	result, code, err := h.proxyGet(r.Context(), server, "/projects")
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

// ProxyMessages handles GET /api/board/remotes/proxy/{remote_server}/{project}/messages/all
func (h *BoardRemotesHandler) ProxyMessages(w http.ResponseWriter, r *http.Request) {
	remoteServer := chi.URLParam(r, "remoteServer")
	project := urlParam(r, "project")
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "200"
	}
	path := fmt.Sprintf("/%s/messages/all?limit=%s", project, limit)
	result, code, err := h.proxyGet(r.Context(), remoteServer, path)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

// ProxySubscribers handles GET /api/board/remotes/proxy/{remote_server}/{project}/subscribers
func (h *BoardRemotesHandler) ProxySubscribers(w http.ResponseWriter, r *http.Request) {
	remoteServer := chi.URLParam(r, "remoteServer")
	project := urlParam(r, "project")
	path := fmt.Sprintf("/%s/subscribers", project)
	result, code, err := h.proxyGet(r.Context(), remoteServer, path)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

// ProxyCheckUnread handles GET /api/board/remotes/proxy/{remote_server}/{project}/messages/check
func (h *BoardRemotesHandler) ProxyCheckUnread(w http.ResponseWriter, r *http.Request) {
	remoteServer := chi.URLParam(r, "remoteServer")
	project := urlParam(r, "project")
	sessionID := r.URL.Query().Get("session_id")
	path := fmt.Sprintf("/%s/messages/check?session_id=%s", project, sessionID)
	result, code, err := h.proxyGet(r.Context(), remoteServer, path)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

// proxyGet forwards a GET request to a remote Coral server's board API with SSRF protection.
func (h *BoardRemotesHandler) proxyGet(ctx context.Context, remoteServer, path string) ([]byte, int, error) {
	// Validate remote server is a registered subscription
	subs, err := h.rbs.ListAllRemoteSubs(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to list subscriptions: %w", err)
	}

	registered := false
	for _, sub := range subs {
		if strings.TrimRight(sub.RemoteServer, "/") == strings.TrimRight(remoteServer, "/") {
			registered = true
			break
		}
	}
	if !registered {
		return nil, http.StatusForbidden, fmt.Errorf("remote server is not registered. Add a subscription first")
	}

	// Resolve and validate IP to prevent SSRF + DNS rebinding
	resolvedIP, err := httputil.ResolveAndValidateURL(remoteServer)
	if err != nil {
		return nil, http.StatusForbidden, err
	}

	// Build pinned URL using resolved IP
	parsed, err := url.Parse(strings.TrimRight(remoteServer, "/"))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid remote server URL")
	}

	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	pinnedURL := fmt.Sprintf("%s://%s:%s/api/board%s", parsed.Scheme, resolvedIP, port, path)

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", pinnedURL, nil)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("failed to create request: %w", err)
	}
	// Set Host header to original hostname for correct routing
	req.Host = parsed.Hostname()

	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("cannot reach remote server %s: %w", remoteServer, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("failed to read remote response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("remote server error: %s", string(body))
	}

	return body, http.StatusOK, nil
}

// SSRF validation uses httputil.ResolveAndValidateURL and httputil.IsIPBlocked.
