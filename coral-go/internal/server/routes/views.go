package routes

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/store"
)

// ViewsHandler handles custom view CRUD endpoints.
type ViewsHandler struct {
	vs *store.ViewStore
}

func NewViewsHandler(db *store.DB) *ViewsHandler {
	return &ViewsHandler{vs: store.NewViewStore(db)}
}

// ListViews returns all custom views.
// GET /api/views
func (h *ViewsHandler) ListViews(w http.ResponseWriter, r *http.Request) {
	views, err := h.vs.ListViews(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(views))
}

// GetView returns a single view.
// GET /api/views/{id}
func (h *ViewsHandler) GetView(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid id")
		return
	}
	view, err := h.vs.GetView(r.Context(), id)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if view == nil {
		errNotFound(w, "view not found")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// viewWriteAllowed admits only the positively identified local dashboard (loopback peer, localhost Host, same-origin
// browser fetch, no tunnel stamp, no Bearer credential) and a JSON content type. What this stops: other websites
// (CSRF), browsers on another origin, proxies, the Cloud Hub, and an agent or script that simply calls the API with curl
// or an HTTP library. What it does NOT stop: a process on this computer that deliberately forges the headers
// (Sec-Fetch-Site and Host are ordinary request headers to a non-browser client). That is why this is one layer of
// several: the view itself runs in an opaque-origin sandbox with no network, the bridge exposes an allowlist of reads,
// and a view not written in the dashboard (created_via "api") needs a click before it runs. A per-install dashboard
// token would close the forged-header gap and is a possible later hardening. It writes the 403/400 itself.
func viewWriteAllowed(w http.ResponseWriter, r *http.Request, needJSON bool) bool {
	if !localDashboard(r) {
		errForbidden(w, "Custom views can only be changed from the Coral dashboard on this computer")
		return false
	}
	if needJSON && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		errBadRequest(w, "Content-Type must be application/json")
		return false
	}
	return true
}

// viewProvenance is "ui" for a write that carried dashboard evidence. Writes without it are refused above, so this is
// belt and braces: the stored marker never claims more than was proven.
func viewProvenance(r *http.Request) string {
	if localDashboard(r) {
		return "ui"
	}
	return "api"
}

// CreateView creates a new custom view.
// POST /api/views
func (h *ViewsHandler) CreateView(w http.ResponseWriter, r *http.Request) {
	if !viewWriteAllowed(w, r, true) {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Prompt   string `json:"prompt"`
		HTML     string `json:"html"`
		TabOrder int    `json:"tab_order"`
		Scope    string `json:"scope"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Name == "" {
		errBadRequest(w, "name is required")
		return
	}
	if body.Scope == "" {
		body.Scope = "global"
	}

	view := &store.CustomView{
		Name:       body.Name,
		Prompt:     body.Prompt,
		HTML:       body.HTML,
		TabOrder:   body.TabOrder,
		Scope:      body.Scope,
		CreatedVia: viewProvenance(r),
	}
	id, err := h.vs.CreateView(r.Context(), view)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Answer with the stored record, so created_at/updated_at (set by the store)
	// are the real ones.
	stored, err := h.vs.GetView(r.Context(), id)
	if err != nil || stored == nil {
		errInternalServer(w, "view was created but could not be read back")
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// UpdateView updates an existing view.
// PUT /api/views/{id}
func (h *ViewsHandler) UpdateView(w http.ResponseWriter, r *http.Request) {
	if !viewWriteAllowed(w, r, true) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid id")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Prompt   string `json:"prompt"`
		HTML     string `json:"html"`
		TabOrder int    `json:"tab_order"`
		Scope    string `json:"scope"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Scope == "" {
		body.Scope = "global"
	}

	view := &store.CustomView{
		Name:       body.Name,
		Prompt:     body.Prompt,
		HTML:       body.HTML,
		TabOrder:   body.TabOrder,
		Scope:      body.Scope,
		CreatedVia: viewProvenance(r),
	}
	if err := h.vs.UpdateView(r.Context(), id, view); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	stored, err := h.vs.GetView(r.Context(), id)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if stored == nil {
		errNotFound(w, "view not found")
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

// DeleteView deletes a view.
// DELETE /api/views/{id}
func (h *ViewsHandler) DeleteView(w http.ResponseWriter, r *http.Request) {
	if !viewWriteAllowed(w, r, false) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid id")
		return
	}
	if err := h.vs.DeleteView(r.Context(), id); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
