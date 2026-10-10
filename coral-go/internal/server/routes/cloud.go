package routes

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// MaxModeSetting is the user setting holding this server's maximum cloud access mode:
// "view" (default), "interact" or "launch". It is local: the connector reads it on every
// request, so neither the hub nor a tunneled request can change it (the /api/cloud
// routes are not in the tunnel route table).
const MaxModeSetting = "cloud_max_mode"

// MaxModeFromSettings reads the local maximum mode. Anything missing or unrecognised is view.
func MaxModeFromSettings(ss *store.SessionStore) func() tunnel.Mode {
	return func() tunnel.Mode {
		s, err := ss.GetSettings(context.Background())
		if err != nil {
			return tunnel.TierView
		}
		if m, ok := tunnel.ParseMode(s[MaxModeSetting]); ok {
			return m
		}
		return tunnel.TierView
	}
}

// CloudHandler serves the "Connect to Coral Hub" settings section.
type CloudHandler struct {
	ss *store.SessionStore

	mu  sync.RWMutex
	mgr *cloud.Manager
}

// NewCloudHandler creates the handler. The manager is attached later (SetManager)
// because it needs the running server's address.
func NewCloudHandler(ss *store.SessionStore) *CloudHandler { return &CloudHandler{ss: ss} }

// SetManager attaches the connector manager.
func (h *CloudHandler) SetManager(m *cloud.Manager) {
	h.mu.Lock()
	h.mgr = m
	h.mu.Unlock()
}

func (h *CloudHandler) manager() *cloud.Manager {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.mgr
}

func (h *CloudHandler) status() cloud.Status {
	if m := h.manager(); m != nil {
		return m.Status()
	}
	return cloud.Status{State: cloud.StateOff}
}

// refuseTunneled admits only the positively identified local dashboard (loopback peer,
// localhost Host, same-origin browser fetch, no tunnel stamp, no Bearer credential). The
// connector's own settings are not reachable by the hub through the tunnel, by a proxy
// holding the API key, or by a stray curl: only someone using Coral on this computer.
func refuseTunneled(w http.ResponseWriter, r *http.Request) bool {
	if cloud.Tunneled(r) || !localDashboard(r) {
		errForbidden(w, "Cloud connector settings are only available from the Coral dashboard on this computer")
		return true
	}
	return false
}

// requireJSON makes state-changing calls non-"simple" cross-site requests, so a web
// page in the user's browser cannot toggle connector settings without a CORS preflight.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		errBadRequest(w, "Content-Type must be application/json")
		return false
	}
	return true
}

// GetStatus: GET /api/cloud/status
func (h *CloudHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if refuseTunneled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// Connect: POST /api/cloud/connect — starts enrollment and returns the user code.
func (h *CloudHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if refuseTunneled(w, r) || !requireJSON(w, r) {
		return
	}
	m := h.manager()
	if m == nil {
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	if err := m.Connect(r.Context()); err != nil {
		if err == cloud.ErrNotConfigured {
			errBadRequest(w, err.Error())
			return
		}
		errInternalServer(w, "Could not start enrollment; check that the hub is reachable")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// Disconnect: POST /api/cloud/disconnect — forgets the token locally.
func (h *CloudHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if refuseTunneled(w, r) || !requireJSON(w, r) {
		return
	}
	m := h.manager()
	if m == nil {
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	if err := m.Disconnect(r.Context()); err != nil {
		errInternalServer(w, "Could not disconnect")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// SetMaxMode: PUT /api/cloud/max-mode {"mode": "view"|"interact"|"launch"}
func (h *CloudHandler) SetMaxMode(w http.ResponseWriter, r *http.Request) {
	if refuseTunneled(w, r) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "mode is required")
		return
	}
	mode, ok := tunnel.ParseMode(body.Mode)
	if !ok {
		errBadRequest(w, "mode must be view, interact or launch")
		return
	}
	if err := h.ss.SetSetting(r.Context(), MaxModeSetting, mode.String()); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	if m := h.manager(); m != nil {
		m.ModeChanged(r.Context())
	}
	writeJSON(w, http.StatusOK, h.status())
}
