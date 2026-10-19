// Pausing and resuming a board (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"log/slog"
	"net/http"
)

func (h *BoardHandler) isPaused(project string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.paused[project]
}

// SetPaused pauses or resumes a board (the Operator's Pause and Resume, sleep and wake). The
// flag is stored with the board, so a restart doesn't un-pause it (#3668, the audit's finding 2).
func (h *BoardHandler) SetPaused(project string, paused bool) {
	h.mu.Lock()
	if paused {
		h.paused[project] = true
	} else {
		delete(h.paused, project)
	}
	h.mu.Unlock()
	if h.bs != nil {
		if err := h.bs.SetBoardPaused(context.Background(), project, paused); err != nil {
			slog.Warn("[board] storing a board's pause", "board", project, "error", err)
		}
	}
}

// LoadPaused restores the boards paused before a restart.
func (h *BoardHandler) LoadPaused(ctx context.Context) {
	if h.bs == nil {
		return
	}
	boards, err := h.bs.PausedBoards(ctx)
	if err != nil {
		slog.Warn("[board] reading the paused boards", "error", err)
		return
	}
	h.mu.Lock()
	for _, b := range boards {
		h.paused[b] = true
	}
	h.mu.Unlock()
}

// IsPaused returns whether a board is paused (exported for use by notifier).
func (h *BoardHandler) IsPaused(project string) bool {
	return h.isPaused(project)
}

// PauseBoard pauses reads for a board.
// POST /api/board/{project}/pause
func (h *BoardHandler) PauseBoard(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	h.SetPaused(project, true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": true})
}

// ResumeBoard resumes reads for a board.
// POST /api/board/{project}/resume
func (h *BoardHandler) ResumeBoard(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	h.SetPaused(project, false)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": false})
}

// GetPaused returns whether a board is paused.
// GET /api/board/{project}/paused
func (h *BoardHandler) GetPaused(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	writeJSON(w, http.StatusOK, map[string]any{"paused": h.isPaused(project)})
}
