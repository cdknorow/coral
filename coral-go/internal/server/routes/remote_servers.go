package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store"
)

// RemoteServersHandler implements the hub-local server registry API
// (/api/servers). These routes are never proxied and never return API keys.
type RemoteServersHandler struct {
	store         *store.RemoteServerStore
	healthTimeout time.Duration
}

// NewRemoteServersHandler creates the handler. The store holds the encrypted keys.
func NewRemoteServersHandler(db *store.DB, cfg *config.Config) *RemoteServersHandler {
	return &RemoteServersHandler{
		store:         store.NewRemoteServerStore(db, cfg.CoralDir()),
		healthTimeout: 10 * time.Second,
	}
}

// Store exposes the registry store (for the proxy and poller).
func (h *RemoteServersHandler) Store() *store.RemoteServerStore { return h.store }

type remoteServerRequest struct {
	ID           string  `json:"id"`
	Label        *string `json:"label"`
	URL          *string `json:"url"`
	APIKey       *string `json:"api_key"`
	AllowPrivate *bool   `json:"allow_private"`
}

// RemoteHealth is the outcome of probing a remote's /api/health.
type RemoteHealth struct {
	Status  string // store.RemoteStatus*
	Version string
	Err     string
}

// normalizeRemoteURL reduces a URL to scheme://host[:port] and rejects
// credentials, paths, queries and fragments.
func normalizeRemoteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("URL scheme must be http or https")
	}
	if u.User != nil {
		return "", errors.New("URL must not contain credentials")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("URL must be scheme://host[:port] with no path, query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

// validateRemoteURL normalizes the URL and applies the SSRF check unless the
// server was explicitly registered with allow_private.
func validateRemoteURL(raw string, allowPrivate bool) (string, error) {
	norm, err := normalizeRemoteURL(raw)
	if err != nil {
		return "", err
	}
	if !allowPrivate {
		if _, err := httputil.ResolveAndValidateURL(norm); err != nil {
			return "", err
		}
	}
	return norm, nil
}

// CheckRemoteHealth calls GET {baseURL}/api/health with the key. Unless
// allowPrivate, the hostname is resolved, validated and then dialled by IP so
// DNS rebinding between check and connect cannot reach a private address.
// Redirects are never followed, so the key cannot be forwarded elsewhere.
func CheckRemoteHealth(ctx context.Context, baseURL, apiKey string, allowPrivate bool, timeout time.Duration) RemoteHealth {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if allowPrivate {
				return dialer.DialContext(ctx, network, addr)
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := httputil.ResolveAndValidateURL("http://" + addr)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/health", nil)
	if err != nil {
		return RemoteHealth{Status: store.RemoteStatusUnreachable, Err: "invalid URL"}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors name the URL, never request headers.
		msg := "remote unreachable"
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			msg = "remote timed out"
		}
		return RemoteHealth{Status: store.RemoteStatusUnreachable, Err: msg}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return RemoteHealth{Status: store.RemoteStatusUnauthorized, Err: "remote rejected API key"}
	case resp.StatusCode != http.StatusOK:
		return RemoteHealth{Status: store.RemoteStatusUnreachable, Err: fmt.Sprintf("remote returned HTTP %d", resp.StatusCode)}
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body); err != nil || body.Status != "ok" {
		return RemoteHealth{Status: store.RemoteStatusUnreachable, Err: "remote is not a Coral server"}
	}
	return RemoteHealth{Status: store.RemoteStatusOnline, Version: body.Version}
}

func localServerEntry() map[string]any {
	return map[string]any{
		"id": store.LocalServerID, "label": "Local", "url": "",
		"status": store.RemoteStatusOnline, "allow_private": false,
		"last_seen": nil, "last_error": nil, "local": true,
	}
}

func decodeRemoteReq(w http.ResponseWriter, r *http.Request) (remoteServerRequest, bool) {
	var req remoteServerRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := decodeJSON(r, &req); err != nil {
		errBadRequest(w, "invalid request body")
		return req, false
	}
	return req, true
}

func remoteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRemoteNotFound):
		errNotFound(w, "server not found")
	case errors.Is(err, store.ErrRemoteExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "server id already exists"})
	case errors.Is(err, store.ErrInvalidRemoteID):
		errBadRequest(w, err.Error())
	case errors.Is(err, store.ErrKeyUnreadable):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "stored remote keys cannot be decrypted; re-enter the API key", "status": store.RemoteStatusKeyUnreadable})
	default:
		slog.Error("remote servers request failed", "error", err.Error())
		errInternalServer(w, "internal error")
	}
}

// List handles GET /api/servers.
func (h *RemoteServersHandler) List(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.List(r.Context())
	if err != nil {
		remoteErr(w, err)
		return
	}
	out := make([]any, 0, len(servers)+1)
	out = append(out, localServerEntry())
	for _, s := range servers {
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, out)
}

// Add handles POST /api/servers.
func (h *RemoteServersHandler) Add(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeRemoteReq(w, r)
	if !ok {
		return
	}
	if err := store.ValidateRemoteID(req.ID); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	if req.URL == nil || req.APIKey == nil || strings.TrimSpace(*req.APIKey) == "" {
		errBadRequest(w, "url and api_key are required")
		return
	}
	label := req.ID
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = strings.TrimSpace(*req.Label)
	}
	allowPrivate := req.AllowPrivate != nil && *req.AllowPrivate
	norm, err := validateRemoteURL(*req.URL, allowPrivate)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	if _, err := h.store.Get(r.Context(), req.ID); err == nil {
		remoteErr(w, store.ErrRemoteExists)
		return
	}
	health := CheckRemoteHealth(r.Context(), norm, *req.APIKey, allowPrivate, h.healthTimeout)
	if health.Status != store.RemoteStatusOnline {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": health.Err, "status": health.Status})
		return
	}
	srv, err := h.store.Add(r.Context(), req.ID, label, norm, *req.APIKey, allowPrivate)
	if err != nil {
		remoteErr(w, err)
		return
	}
	h.store.SetStatus(r.Context(), srv.ID, store.RemoteStatusOnline, "")
	srv, _ = h.store.Get(r.Context(), srv.ID)
	writeJSON(w, http.StatusCreated, srv)
}

// Update handles PATCH /api/servers/{id}.
func (h *RemoteServersHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := urlParam(r, "id")
	cur, err := h.store.Get(r.Context(), id)
	if err != nil {
		remoteErr(w, err)
		return
	}
	req, ok := decodeRemoteReq(w, r)
	if !ok {
		return
	}
	upd := store.RemoteServerUpdate{AllowPrivate: req.AllowPrivate}
	if req.Label != nil {
		l := strings.TrimSpace(*req.Label)
		if l == "" {
			errBadRequest(w, "label must not be empty")
			return
		}
		upd.Label = &l
	}
	if req.APIKey != nil {
		if strings.TrimSpace(*req.APIKey) == "" {
			errBadRequest(w, "api_key must not be empty")
			return
		}
		upd.APIKey = req.APIKey
	}
	allowPrivate := cur.AllowPrivate
	if req.AllowPrivate != nil {
		allowPrivate = *req.AllowPrivate
	}
	newURL := cur.URL
	if req.URL != nil {
		newURL = *req.URL
	}
	norm, err := validateRemoteURL(newURL, allowPrivate)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	if req.URL != nil {
		upd.URL = &norm
	}
	// Re-verify connectivity and auth whenever the target or credential changes.
	if req.URL != nil || req.APIKey != nil || (req.AllowPrivate != nil && *req.AllowPrivate != cur.AllowPrivate) {
		key := ""
		if req.APIKey != nil {
			key = *req.APIKey
		} else if key, err = h.store.DecryptedKey(r.Context(), id); err != nil {
			remoteErr(w, err)
			return
		}
		health := CheckRemoteHealth(r.Context(), norm, key, allowPrivate, h.healthTimeout)
		if health.Status != store.RemoteStatusOnline {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": health.Err, "status": health.Status})
			return
		}
	}
	srv, err := h.store.Update(r.Context(), id, upd)
	if err != nil {
		remoteErr(w, err)
		return
	}
	if req.URL != nil || req.APIKey != nil {
		h.store.SetStatus(r.Context(), id, store.RemoteStatusOnline, "")
		srv, _ = h.store.Get(r.Context(), id)
	}
	writeJSON(w, http.StatusOK, srv)
}

// Delete handles DELETE /api/servers/{id}.
func (h *RemoteServersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), urlParam(r, "id")); err != nil {
		remoteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Test handles POST /api/servers/{id}/test and records the outcome.
func (h *RemoteServersHandler) Test(w http.ResponseWriter, r *http.Request) {
	id := urlParam(r, "id")
	cur, err := h.store.Get(r.Context(), id)
	if err != nil {
		remoteErr(w, err)
		return
	}
	key, err := h.store.DecryptedKey(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrKeyUnreadable) {
			h.store.SetStatus(r.Context(), id, store.RemoteStatusKeyUnreadable, "stored key cannot be decrypted")
			srv, _ := h.store.Get(r.Context(), id)
			writeJSON(w, http.StatusOK, srv)
			return
		}
		remoteErr(w, err)
		return
	}
	var health RemoteHealth
	if _, verr := validateRemoteURL(cur.URL, cur.AllowPrivate); verr != nil {
		health = RemoteHealth{Status: store.RemoteStatusUnreachable, Err: verr.Error()}
	} else {
		health = CheckRemoteHealth(r.Context(), cur.URL, key, cur.AllowPrivate, h.healthTimeout)
	}
	h.store.SetStatus(r.Context(), id, health.Status, health.Err)
	srv, _ := h.store.Get(r.Context(), id)
	writeJSON(w, http.StatusOK, srv)
}
