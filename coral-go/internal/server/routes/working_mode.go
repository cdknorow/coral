package routes

import (
	"errors"
	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (h *BoardHandler) GetWorkingMode(w http.ResponseWriter, r *http.Request) {
	mode, err := h.bs.GetWorkingMode(r.Context(), chi.URLParam(r, "project"))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mode)
}
func (h *BoardHandler) SetWorkingMode(w http.ResponseWriter, r *http.Request) {
	var mode board.WorkingMode
	if err := decodeJSON(r, &mode); err != nil {
		errBadRequest(w, "invalid working mode JSON")
		return
	}
	result, err := h.bs.SetWorkingMode(r.Context(), chi.URLParam(r, "project"), mode)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Presets are team guidance editable by users and agents, not task assignments.
func (h *BoardHandler) ListWorkflowPresets(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	presets, err := h.bs.ListWorkflowPresets(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	mode, err := h.bs.GetWorkingMode(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": presets, "working_mode": mode})
}
func (h *BoardHandler) SaveWorkflowPreset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid preset JSON")
		return
	}
	create := r.Method == http.MethodPost
	id := chi.URLParam(r, "presetID")
	if create {
		id = body.ID
	}
	p, err := h.bs.SaveWorkflowPreset(r.Context(), chi.URLParam(r, "project"), id, body.Name, body.Instructions, create)
	if err != nil {
		if errors.Is(err, board.ErrPresetExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			errBadRequest(w, err.Error())
		}
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	writeJSON(w, status, p)
}
func (h *BoardHandler) ResetWorkflowPreset(w http.ResponseWriter, r *http.Request) {
	p, err := h.bs.ResetWorkflowPreset(r.Context(), chi.URLParam(r, "project"), chi.URLParam(r, "presetID"))
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}
