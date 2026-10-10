package routes

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
)

// AllowLaunchSetting is the user setting behind "allow launching from the web".
// It is local: the connector reads it on every launch-class request, so neither
// the hub nor a tunneled request can change it (the /api/cloud routes are not on
// the tunnel allowlist).
const AllowLaunchSetting = "cloud_allow_launch"

// AllowLaunchFromSettings reports whether launching from the web is switched on.
func AllowLaunchFromSettings(ss *store.SessionStore) func() bool {
	return func() bool {
		s, err := ss.GetSettings(context.Background())
		return err == nil && strings.EqualFold(s[AllowLaunchSetting], "true")
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

// refuseTunneled stops requests that arrived through the hub tunnel: the
// connector's own settings are never remotely configurable.
func refuseTunneled(w http.ResponseWriter, r *http.Request) bool {
	if cloud.Tunneled(r) {
		errForbidden(w, "Not available through the cloud hub")
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

// SetAllowLaunch: PUT /api/cloud/allow-launch {"allow": bool}
func (h *CloudHandler) SetAllowLaunch(w http.ResponseWriter, r *http.Request) {
	if refuseTunneled(w, r) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Allow *bool `json:"allow"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Allow == nil {
		errBadRequest(w, "allow (boolean) is required")
		return
	}
	v := "false"
	if *body.Allow {
		v = "true"
	}
	if err := h.ss.SetSetting(r.Context(), AllowLaunchSetting, v); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}
