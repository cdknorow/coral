package routes

import (
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
