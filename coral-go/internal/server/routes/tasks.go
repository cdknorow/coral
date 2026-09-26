package routes

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/store"
)

// TasksHandler handles one-shot task run endpoints.
type TasksHandler struct {
	sched     *store.ScheduleStore
	cfg       *config.Config
	scheduler *background.JobScheduler
	proxy     *proxy.Store
}

func NewTasksHandler(db *store.DB, cfg *config.Config) *TasksHandler {
	return &TasksHandler{
		sched: store.NewScheduleStore(db),
		cfg:   cfg,
	}
}

// SetScheduler injects the job scheduler for task launching/killing.
func (h *TasksHandler) SetScheduler(s *background.JobScheduler) {
	h.scheduler = s
}

// SetProxyStore injects the proxy request store for task-level cost rollups.
func (h *TasksHandler) SetProxyStore(ps *proxy.Store) {
	h.proxy = ps
}

// SubmitTask creates a one-shot task run.
// POST /api/tasks/run
func (h *TasksHandler) SubmitTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt          string `json:"prompt"`
		RepoPath        string `json:"repo_path"`
		AgentType       string `json:"agent_type"`
		BaseBranch      string `json:"base_branch"`
		CreateWorktree  *bool  `json:"create_worktree"`
		MaxDurationS    int    `json:"max_duration_s"`
		CleanupWorktree *bool  `json:"cleanup_worktree"`
		Flags           string `json:"flags"`
		DisplayName     string `json:"display_name"`
		WebhookURL      string `json:"webhook_url"`
		AutoAccept      bool   `json:"auto_accept"`
		MaxAutoAccepts  int    `json:"max_auto_accepts"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		errBadRequest(w, "'prompt' is required")
		return
	}
	if strings.TrimSpace(body.RepoPath) == "" {
		errBadRequest(w, "'repo_path' is required")
		return
	}
	if info, err := os.Stat(body.RepoPath); err != nil || !info.IsDir() {
		errBadRequest(w, "repo_path '"+body.RepoPath+"' does not exist")
		return
	}

	// Apply defaults
	if body.AgentType == "" {
		body.AgentType = "claude"
	}
	if body.BaseBranch == "" {
		body.BaseBranch = "main"
	}
	if body.MaxDurationS == 0 {
		body.MaxDurationS = 3600
	}
	if body.MaxAutoAccepts == 0 {
		body.MaxAutoAccepts = 10
	}

	// Auto-accept flag injection (agent-type-aware)
	flags := strings.TrimSpace(body.Flags)
	if body.AutoAccept {
		var skipFlag string
		switch body.AgentType {
		case at.Codex:
			skipFlag = "--full-auto"
		case at.Agy, at.Antigravity, at.Gemini:
			skipFlag = "--dangerously-skip-permissions"
		default:
			skipFlag = "--dangerously-skip-permissions"
		}
		if !strings.Contains(flags, skipFlag) {
			flags = strings.TrimSpace(skipFlag + " " + flags)
		}
	}

	createWT := true
	if body.CreateWorktree != nil {
		createWT = *body.CreateWorktree
	}
	cleanupWT := true
	if body.CleanupWorktree != nil {
		cleanupWT = *body.CleanupWorktree
	}

	launchConfig := map[string]interface{}{
		"repo_path":        body.RepoPath,
		"base_branch":      body.BaseBranch,
		"agent_type":       body.AgentType,
		"prompt":           body.Prompt,
		"display_name":     body.DisplayName,
		"flags":            flags,
		"max_duration_s":   float64(body.MaxDurationS),
		"cleanup_worktree": cleanupWT,
		"create_worktree":  createWT,
		"webhook_url":      body.WebhookURL,
		"auto_accept":      body.AutoAccept,
		"max_auto_accepts": float64(body.MaxAutoAccepts),
	}

	if h.scheduler != nil {
		runID, err := h.scheduler.FireOneshot(r.Context(), launchConfig)
		if err != nil {
			if _, ok := err.(*background.ConcurrencyLimitError); ok {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
				return
			}
			errInternalServer(w, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "status": "pending"})
		return
	}

	// Fallback: just create a DB record if no scheduler is wired
	now := store.NowUTC()
	var webhookURL, displayName *string
	if body.WebhookURL != "" {
		webhookURL = &body.WebhookURL
	}
	if body.DisplayName != "" {
		displayName = &body.DisplayName
	}
	runID, err := h.sched.CreateOneshotRun(r.Context(), now, displayName, webhookURL)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "status": "pending"})
}

// GetTaskStatus returns the status of a one-shot run.
// GET /api/tasks/runs/{runID}
func (h *TasksHandler) GetTaskStatus(w http.ResponseWriter, r *http.Request) {
	runID, _ := strconv.ParseInt(chi.URLParam(r, "runID"), 10, 64)
	run, err := h.sched.GetScheduledRun(r.Context(), runID)
	if err != nil {
		errNotFound(w, "run not found")
		return
	}
	h.attachProxyCost(r.Context(), run)
	writeJSON(w, http.StatusOK, run)
}

// KillTask kills a running one-shot task.
// POST /api/tasks/runs/{runID}/kill
func (h *TasksHandler) KillTask(w http.ResponseWriter, r *http.Request) {
	runID, _ := strconv.ParseInt(chi.URLParam(r, "runID"), 10, 64)

	if h.scheduler != nil {
		if !h.scheduler.KillRun(r.Context(), runID) {
			errNotFound(w, "Run not found or not active")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Fallback: just update DB
	run, err := h.sched.GetScheduledRun(r.Context(), runID)
	if err != nil || run == nil {
		errNotFound(w, "Run not found or not active")
		return
	}
	h.sched.UpdateScheduledRun(r.Context(), runID, map[string]interface{}{
		"status":      "killed",
		"exit_reason": "user_cancelled",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListTasks returns recent one-shot runs.
// GET /api/tasks/runs
func (h *TasksHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	status := r.URL.Query().Get("status")
	var statusPtr *string
	if status != "" {
		statusPtr = &status
	}
	runs, err := h.sched.ListOneshotRuns(r.Context(), limit, statusPtr)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.attachProxyCosts(r.Context(), runs)
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

// ListActiveRuns returns currently running tasks.
// GET /api/tasks/active
func (h *TasksHandler) ListActiveRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := h.sched.ListActiveRuns(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.attachProxyCosts(r.Context(), runs)
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

func (h *TasksHandler) attachProxyCost(ctx context.Context, run *store.ScheduledRun) {
	if h.proxy == nil || run == nil {
		return
	}
	cost, err := h.proxy.GetTaskRunCost(ctx, run.ID)
	if err != nil {
		return
	}
	run.ProxyCostUSD = cost.TotalCostUSD
	run.ProxyRequestCount = cost.TotalRequests
	run.ProxyInputTokens = cost.TotalInputTokens
	run.ProxyOutputTokens = cost.TotalOutputTokens
}

func (h *TasksHandler) attachProxyCosts(ctx context.Context, runs []store.ScheduledRun) {
	if h.proxy == nil || len(runs) == 0 {
		return
	}
	runIDs := make([]int64, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	costs, err := h.proxy.GetTaskRunCosts(ctx, runIDs)
	if err != nil {
		return
	}
	for i := range runs {
		if cost, ok := costs[runs[i].ID]; ok {
			runs[i].ProxyCostUSD = cost.TotalCostUSD
			runs[i].ProxyRequestCount = cost.TotalRequests
			runs[i].ProxyInputTokens = cost.TotalInputTokens
			runs[i].ProxyOutputTokens = cost.TotalOutputTokens
		}
	}
}
