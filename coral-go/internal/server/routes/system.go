package routes

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agentmodels"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// Indexer is the interface for triggering an index refresh.
type Indexer interface {
	RunOnce(ctx context.Context) error
}

// SystemHandler handles settings, tags, and filesystem endpoints.
type SystemHandler struct {
	// models lists each agent CLI's models (#4065).
	models  *agentmodels.Service
	db      *store.DB
	ss      *store.SessionStore
	cfg     *config.Config
	indexer Indexer

	// boardAuthor reads who wrote a board message, for the bookmarks saved before the author was kept (#4792); set by the server wiring.
	boardAuthor func(ctx context.Context, project string, id int64) (string, bool, error)
	// savedBoardText and savedAgentText read the source of a bookmark for "Read the whole message" (#5753): a board message by its board and id (found false when
	// it is gone or is not on that board), an agent's message by its session id, agent type and key (reachable false when this machine has no transcript of it).
	savedBoardText func(ctx context.Context, project string, id int64) (text string, found bool, err error)
	savedAgentText func(ctx context.Context, sessionID, agentType, key string) (text string, found, reachable bool)
	// savedBoardHas and savedAgentHas say, for the list (#5802), which of a board's message ids exist (one query per board) and which of an agent's message keys are in its
	// transcript (one read per agent), so the client asks nothing per bookmark.
	savedBoardHas func(ctx context.Context, project string, ids []int64) (map[int64]bool, error)
	savedAgentHas func(ctx context.Context, sessionID, agentType string, keys []string) (found map[string]bool, reachable, known bool)

	generateJobs   map[string]*generateJob
	generateJobsMu sync.Mutex

	// terminal identifies the terminal backend this server actually runs, so
	// status can report the effective backend (set by the server wiring).
	terminal ptymanager.SessionTerminal

	// tmux discovery can start a login shell when tmux is not on PATH, so
	// routine status polls reuse a recent result; an explicit re-check bypasses
	// the cache. tmuxCacheTTL 0 means the default.
	tmuxMu       sync.Mutex
	tmuxAt       time.Time
	tmuxPath     string
	tmuxFound    bool
	tmuxCacheTTL time.Duration
}

// SetTerminal tells the handler which terminal backend the server is running.
func (h *SystemHandler) SetTerminal(t ptymanager.SessionTerminal) { h.terminal = t }

// effectiveBackend reports the backend in use: "tmux", "pty", or "unknown".
func (h *SystemHandler) effectiveBackend() string { return TerminalKind(h.terminal) }

// TerminalKind is "tmux", "pty" or "unknown" for a session terminal.
func TerminalKind(t ptymanager.SessionTerminal) string {
	switch t.(type) {
	case *ptymanager.TmuxSessionTerminal:
		return "tmux"
	case *ptymanager.PTYSessionTerminal:
		return "pty"
	}
	return "unknown"
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(db *store.DB, cfg *config.Config) *SystemHandler {
	h := &SystemHandler{
		db:           db,
		ss:           store.NewSessionStore(db),
		cfg:          cfg,
		generateJobs: make(map[string]*generateJob),
	}
	h.models = h.newAgentModels()
	return h
}

// SetIndexer injects the session indexer for manual refresh triggers.
func (h *SystemHandler) SetIndexer(idx Indexer) {
	h.indexer = idx
}

// ── Settings ────────────────────────────────────────────────────────────

// ── Default Prompt Constants ─────────────────────────────────────────
// These reference the authoritative constants in the agent package so
// the frontend defaults stay in sync with the runtime prompts.

// RefreshIndexer triggers a manual re-index.
// POST /api/indexer/refresh
func (h *SystemHandler) RefreshIndexer(w http.ResponseWriter, r *http.Request) {
	if h.indexer == nil {
		errInternalServer(w, "Indexer not available")
		return
	}
	if err := h.indexer.RunOnce(r.Context()); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Tags ────────────────────────────────────────────────────────────────

// ── Folder Tags ─────────────────────────────────────────────────────────

// ── Team Import ─────────────────────────────────────────────────────────

// ── Team Generation (async) ─────────────────────────────────────────
