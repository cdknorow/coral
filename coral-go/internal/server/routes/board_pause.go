// Pausing and resuming a board (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/subgentic/coral/internal/board"
)

func (h *BoardHandler) isPaused(project string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.paused[project]
}

// SetPaused pauses or resumes a board (the Operator's Pause and Resume, and a wake). The
// flag is stored with the board, so a restart doesn't un-pause it (#3668, the audit's finding 2).
// A pause set here is a person's: auto-wake keeps it (#286).
func (h *BoardHandler) SetPaused(project string, paused bool) {
	h.pauseMu.Lock()
	defer h.pauseMu.Unlock()
	h.setPaused(project, paused, false)
}

// PauseForSleep pauses a board because its agents sleep (a team sleep, or Coral's restart with agents left sleeping), a pause
// auto-wake lifts (LiftSleepPause). A board already paused keeps its pause as it is: a person's stays a person's (#286).
func (h *BoardHandler) PauseForSleep(project string) {
	h.pauseMu.Lock() // the check and the pause as one step: a person's Pause in between is never stored as sleep's
	defer h.pauseMu.Unlock()
	h.mu.RLock()
	already := h.paused[project]
	h.mu.RUnlock()
	if !already {
		h.setPaused(project, true, true)
	}
}

// LiftSleepPause resumes a board whose pause sleep or Coral's restart put there, when auto-wake has woken one of its agents
// (as the dashboard's Wake resumes the board, #286). A person's Pause is kept. It reports whether it resumed the board.
func (h *BoardHandler) LiftSleepPause(project string) bool {
	h.pauseMu.Lock() // the check and the resume as one step: a person's Pause in between is never resumed
	defer h.pauseMu.Unlock()
	h.mu.RLock()
	bySleep := h.paused[project] && h.sleepPaused[project]
	h.mu.RUnlock()
	if bySleep {
		h.setPaused(project, false, false)
	}
	return bySleep
}

// setPaused changes the pause in memory and in the store; the caller holds pauseMu, so the stored row always ends as the maps do.
func (h *BoardHandler) setPaused(project string, paused, bySleep bool) {
	h.mu.Lock()
	if paused {
		h.paused[project] = true
	} else {
		delete(h.paused, project)
	}
	if paused && bySleep {
		h.sleepPaused[project] = true
	} else {
		delete(h.sleepPaused, project)
	}
	h.mu.Unlock()
	if h.bs != nil {
		by := ""
		if bySleep {
			by = board.PausedBySleep
		}
		if err := h.bs.SetBoardPausedBy(context.Background(), project, paused, by); err != nil {
			slog.Warn("[board] storing a board's pause", "board", project, "error", err)
		}
	}
}

// LoadPaused restores the boards paused before a restart, and who paused each.
func (h *BoardHandler) LoadPaused(ctx context.Context) {
	if h.bs == nil {
		return
	}
	boards, err := h.bs.PausedBoardsBy(ctx)
	if err != nil {
		slog.Warn("[board] reading the paused boards", "error", err)
		return
	}
	h.mu.Lock()
	for b, by := range boards {
		h.paused[b] = true
		if by == board.PausedBySleep {
			h.sleepPaused[b] = true
		}
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
