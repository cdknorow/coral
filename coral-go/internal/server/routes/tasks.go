package routes

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/store"
)

// TasksHandler handles one-shot task run endpoints.
type TasksHandler struct {
	ss        *store.SessionStore
	sched     *store.ScheduleStore
	cfg       *config.Config
	scheduler *background.JobScheduler
	proxy     *proxy.Store
}

func NewTasksHandler(db *store.DB, cfg *config.Config) *TasksHandler {
	return &TasksHandler{
		ss:    store.NewSessionStore(db),
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
	if body.WebhookURL != "" { // webhooks were removed (#3646)
		errBadRequest(w, errWebhooksNotSupported)
		return
	}
	if strings.TrimSpace(body.RepoPath) == "" {
		errBadRequest(w, "'repo_path' is required")
		return
	}
	// A network path is refused before anything stats it: on Windows the stat alone connects to the host as the user, and tasks/run is
	// reachable at Coral Hub's Launch tier (#126 F1). canonicalizeRepoPath (in jobRepoError) resolves the path; nothing else touches it first.
	if safefs.RefuseRemote(body.RepoPath) != nil {
		errBadRequest(w, "a network path can't be a repo_path")
		return
	}
	// A git work tree, as for scheduled jobs (#3171, #3377): the run makes a worktree in it.
	if msg := jobRepoError(r.Context(), body.RepoPath); msg != "" {
		if cloud.Tunneled(r) { // the path names this computer's folders; the caller has it already
			msg = strings.TrimSuffix(msg, ": "+body.RepoPath)
		}
		errBadRequest(w, msg)
		return
	}
	// Through the Coral Hub, an unattended run that skips permission prompts (auto_accept,
	// or CLI flags that do, agent.SkipsPermissionPrompts) follows the agent auto-accept rule:
	// the local allow-hub-auto-accept switch and the Launch mode (#3377, SEC-JB-01; #126 F4).
	runType := body.AgentType
	if runType == "" {
		runType = "claude"
	}
	if refuseHubSkip(w, r, h.ss, body.AutoAccept || agent.SkipsPermissionPrompts(runType, strings.Fields(body.Flags))) {
		return
	}

	// Apply defaults
	if body.AgentType == "" {
		body.AgentType = "claude"
	}
	if body.BaseBranch == "" {
		body.BaseBranch = "main"
	}
	if err := gitutil.ValidRef(body.BaseBranch); err != nil { // #3601, weak site W24
		errBadRequest(w, "base_branch: "+err.Error())
		return
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
		"auto_accept":      body.AutoAccept,
		"max_auto_accepts": float64(body.MaxAutoAccepts),
	}

	if h.scheduler != nil {
		runID, err := h.scheduler.FireOneshot(r.Context(), launchConfig)
		if err != nil {
			if _, ok := err.(*background.ConcurrencyLimitError); ok {
				httputil.WriteActionLimit(w, 30, "too_many_runs", err.Error()) // a limit on this action (#3434)
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
	var displayName *string
	if body.DisplayName != "" {
		displayName = &body.DisplayName
	}
	runID, err := h.sched.CreateOneshotRun(r.Context(), now, displayName)
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
	limit := clampLimit(queryInt(r, "limit", 50), 50, 200)
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
