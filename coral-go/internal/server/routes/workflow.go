package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/mediatype"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/store"
)

var workflowNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// WorkflowHandler handles workflow API endpoints.
type WorkflowHandler struct {
	ws     *store.WorkflowStore
	cfg    *config.Config
	runner *background.WorkflowRunner
}

// SetWorkflowRunner injects the workflow runner for triggering and killing runs.
func (h *WorkflowHandler) SetWorkflowRunner(wr *background.WorkflowRunner) {
	h.runner = wr
}

// NewWorkflowHandler creates a new WorkflowHandler.
func NewWorkflowHandler(db *store.DB, cfg *config.Config) *WorkflowHandler {
	return &WorkflowHandler{
		ws:  store.NewWorkflowStore(db),
		cfg: cfg,
	}
}

// SeedDemoWorkflow creates the daily-motivation demo workflow and scheduled job on first run.
func (h *WorkflowHandler) SeedDemoWorkflow(schedStore *store.ScheduleStore) {
	h.ws.SeedDemoWorkflow(context.Background(), schedStore)
}

// ── Workflow CRUD ─────────────────────────────────────────────────────

// ListWorkflows returns all workflows with last_run summary.
// GET /api/workflows
func (h *WorkflowHandler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	workflows, err := h.ws.ListWorkflows(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": emptyIfNil(workflows)})
}

// GetWorkflow returns a single workflow by ID.
// GET /api/workflows/{workflowID}
func (h *WorkflowHandler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	getByID(w, r, "workflowID", "invalid workflow ID", "workflow not found", h.ws.GetWorkflow)
}

// GetWorkflowByName returns a single workflow by name.
// GET /api/workflows/by-name/{name}
func (h *WorkflowHandler) GetWorkflowByName(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	wf, err := h.ws.GetWorkflowByName(r.Context(), name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if wf == nil {
		errNotFound(w, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

// CreateWorkflow creates a new workflow.
// POST /api/workflows
func (h *WorkflowHandler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string            `json:"name"`
		Description     string            `json:"description"`
		Steps           []json.RawMessage `json:"steps"`
		DefaultAgent    json.RawMessage   `json:"default_agent"`
		RepoPath        string            `json:"repo_path"`
		BaseBranch      string            `json:"base_branch"`
		MaxDurationS    *int              `json:"max_duration_s"`
		CleanupWorktree *int              `json:"cleanup_worktree"`
		Enabled         *int              `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}

	// Validate name
	if body.Name == "" {
		errBadRequest(w, "name is required")
		return
	}
	if !workflowNameRe.MatchString(body.Name) || safefs.CheckName(body.Name) != nil { // Windows device names (#3671)
		errBadRequest(w, "name contains invalid characters")
		return
	}

	// Check name uniqueness
	existing, _ := h.ws.GetWorkflowByName(r.Context(), body.Name)
	if existing != nil {
		errConflict(w, "workflow name already exists")
		return
	}

	// Validate steps
	if len(body.Steps) == 0 {
		errBadRequest(w, "at least one step is required")
		return
	}
	if len(body.Steps) > 20 {
		errBadRequest(w, "maximum 20 steps allowed")
		return
	}

	// Validate step contents
	if errMsg := validateSteps(body.Steps, body.DefaultAgent); errMsg != "" {
		errBadRequest(w, errMsg)
		return
	}

	// Validate and canonicalize repo_path if provided
	if body.RepoPath != "" {
		resolved, err := canonicalizeRepoPath(body.RepoPath)
		if err != nil {
			errBadRequest(w, repoPathError(err))
			return
		}
		body.RepoPath = resolved
	}

	// Validate max_duration_s
	if body.MaxDurationS != nil && (*body.MaxDurationS <= 0 || *body.MaxDurationS > 86400) {
		errBadRequest(w, "invalid max_duration")
		return
	}

	// Marshal steps and default_agent to JSON strings
	stepsJSON, err := store.StepsJSONFromRaw(body.Steps)
	if err != nil {
		errBadRequest(w, "invalid steps")
		return
	}
	var defaultAgentJSON string
	if body.DefaultAgent != nil {
		defaultAgentJSON = string(body.DefaultAgent)
	}

	if body.BaseBranch != "" {
		if err := gitutil.ValidRef(body.BaseBranch); err != nil { // #3601, weak site W24
			errBadRequest(w, "base_branch: "+err.Error())
			return
		}
	}
	wf := &store.Workflow{
		Name:             body.Name,
		Description:      body.Description,
		StepsJSON:        stepsJSON,
		DefaultAgentJSON: defaultAgentJSON,
		RepoPath:         body.RepoPath,
		BaseBranch:       body.BaseBranch,
		MaxDurationS:     intPtrOr(body.MaxDurationS, 3600),
		CleanupWorktree:  intPtrOr(body.CleanupWorktree, 1),
		Enabled:          intPtrOr(body.Enabled, 1),
	}

	created, err := h.ws.CreateWorkflow(r.Context(), wf)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			errConflict(w, "workflow name already exists")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, withWarnings(created, stepWarnings(body.Steps)))
}

// UpdateWorkflow updates an existing workflow.
// PUT /api/workflows/{workflowID}
func (h *WorkflowHandler) UpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	workflowID, err := strconv.ParseInt(chi.URLParam(r, "workflowID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid workflow ID")
		return
	}

	existing, err := h.ws.GetWorkflow(r.Context(), workflowID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if existing == nil {
		errNotFound(w, "workflow not found")
		return
	}

	// Decode as raw map for dynamic update
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}

	fields := make(map[string]interface{})

	// Handle name update with validation
	if nameRaw, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			errBadRequest(w, "invalid name value")
			return
		}
		if !workflowNameRe.MatchString(name) || safefs.CheckName(name) != nil {
			errBadRequest(w, "name contains invalid characters")
			return
		}
		fields["name"] = name
	}

	// Handle simple string fields
	for _, key := range []string{"description", "repo_path", "base_branch"} {
		if v, ok := raw[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				errBadRequest(w, "invalid value for "+key)
				return
			}
			fields[key] = s
		}
	}
	if b, ok := fields["base_branch"].(string); ok && b != "" {
		if err := gitutil.ValidRef(b); err != nil { // the same check as create (#3601, W24; Security's SHOULD)
			errBadRequest(w, "base_branch: "+err.Error())
			return
		}
	}

	// Handle repo_path validation and canonicalization
	if rp, ok := fields["repo_path"]; ok {
		if rpStr, ok := rp.(string); ok && rpStr != "" {
			resolved, err := canonicalizeRepoPath(rpStr)
			if err != nil {
				errBadRequest(w, repoPathError(err))
				return
			}
			fields["repo_path"] = resolved
		}
	}

	// Handle int fields
	for _, key := range []string{"max_duration_s", "cleanup_worktree", "enabled"} {
		if v, ok := raw[key]; ok {
			var n int
			if err := json.Unmarshal(v, &n); err != nil {
				errBadRequest(w, "invalid value for "+key)
				return
			}
			fields[key] = n
		}
	}

	// Validate max_duration_s
	if v, ok := fields["max_duration_s"]; ok {
		if n, ok := v.(int); ok && (n <= 0 || n > 86400) {
			errBadRequest(w, "invalid max_duration")
			return
		}
	}

	// Handle steps
	var warnings []string
	if stepsRaw, ok := raw["steps"]; ok {
		var steps []json.RawMessage
		if err := json.Unmarshal(stepsRaw, &steps); err != nil {
			errBadRequest(w, "invalid steps")
			return
		}
		if len(steps) == 0 {
			errBadRequest(w, "at least one step is required")
			return
		}
		if len(steps) > 20 {
			errBadRequest(w, "maximum 20 steps allowed")
			return
		}

		// Resolve default_agent for validation
		defaultAgent := json.RawMessage(existing.DefaultAgentJSON)
		if daRaw, ok := raw["default_agent"]; ok {
			defaultAgent = daRaw
		}
		if errMsg := validateSteps(steps, defaultAgent); errMsg != "" {
			errBadRequest(w, errMsg)
			return
		}
		warnings = stepWarnings(steps)

		stepsJSON, err := store.StepsJSONFromRaw(steps)
		if err != nil {
			errBadRequest(w, "invalid steps")
			return
		}
		fields["steps_json"] = stepsJSON
	}

	// Handle default_agent
	if daRaw, ok := raw["default_agent"]; ok {
		fields["default_agent_json"] = string(daRaw)
	}

	updated, err := h.ws.UpdateWorkflow(r.Context(), workflowID, fields)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			errConflict(w, "workflow name already exists")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if updated == nil {
		errNotFound(w, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, withWarnings(updated, warnings))
}

// DeleteWorkflow deletes a workflow and its run history.
// DELETE /api/workflows/{workflowID}
func (h *WorkflowHandler) DeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	workflowID, err := strconv.ParseInt(chi.URLParam(r, "workflowID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid workflow ID")
		return
	}
	// Its runs' directories go with it (audit MED-04): stop any still running,
	// delete the workflow (its run records cascade), then remove the directories.
	runIDs, err := h.ws.WorkflowRunIDs(r.Context(), workflowID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.runner != nil {
		if active, err := h.ws.GetActiveRunsForWorkflow(r.Context(), workflowID); err == nil {
			for _, run := range active {
				h.runner.KillRun(r.Context(), run.ID)
			}
		}
	}
	if err := h.ws.DeleteWorkflow(r.Context(), workflowID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	background.RemoveRunDirs(h.cfg.CoralDir(), runIDs)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Trigger & Runs ───────────────────────────────────────────────────

// TriggerWorkflow triggers a workflow run by ID.
// POST /api/workflows/{workflowID}/trigger
func (h *WorkflowHandler) TriggerWorkflow(w http.ResponseWriter, r *http.Request) {
	workflowID, err := strconv.ParseInt(chi.URLParam(r, "workflowID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid workflow ID")
		return
	}
	wf, err := h.ws.GetWorkflow(r.Context(), workflowID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if wf == nil {
		errNotFound(w, "workflow not found")
		return
	}
	h.triggerWorkflow(w, r, wf)
}

// TriggerWorkflowByName triggers a workflow run by name.
// POST /api/workflows/by-name/{name}/trigger
func (h *WorkflowHandler) TriggerWorkflowByName(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	wf, err := h.ws.GetWorkflowByName(r.Context(), name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if wf == nil {
		errNotFound(w, "workflow not found")
		return
	}
	h.triggerWorkflow(w, r, wf)
}

func (h *WorkflowHandler) triggerWorkflow(w http.ResponseWriter, r *http.Request, wf *store.Workflow) {
	if wf.Enabled == 0 {
		errConflict(w, "workflow is disabled")
		return
	}

	var body struct {
		TriggerType string          `json:"trigger_type"`
		Context     json.RawMessage `json:"context"`
	}
	// Body is optional for trigger
	decodeJSON(r, &body)

	if body.TriggerType == "" {
		body.TriggerType = "api"
	}

	var triggerCtx *string
	if body.Context != nil {
		s := string(body.Context)
		triggerCtx = &s
	}

	// Use the runner if available (it creates the run and starts execution)
	if h.runner != nil {
		run, err := h.runner.TriggerWorkflow(r.Context(), wf, body.TriggerType, triggerCtx)
		if err != nil {
			var limit *background.ConcurrencyLimitError
			if errors.As(err, &limit) {
				httputil.WriteActionLimit(w, 30, "too_many_runs", fmt.Sprintf("%d workflow runs are already in progress; try again when one finishes", limit.Limit)) // #3434
				return
			}
			errInternalServer(w, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"run_id":        run.ID,
			"workflow_id":   wf.ID,
			"workflow_name": wf.Name,
			"status":        run.Status,
			"trigger_type":  run.TriggerType,
			"created_at":    run.CreatedAt,
		})
		return
	}

	// Fallback: create run record only (no execution)
	run, err := h.ws.CreateWorkflowRun(r.Context(), wf.ID, body.TriggerType, triggerCtx)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":        run.ID,
		"workflow_id":   wf.ID,
		"workflow_name": wf.Name,
		"status":        run.Status,
		"trigger_type":  run.TriggerType,
		"created_at":    run.CreatedAt,
	})
}

// ListWorkflowRuns returns runs for a specific workflow.
// GET /api/workflows/{workflowID}/runs
func (h *WorkflowHandler) ListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	workflowID, err := strconv.ParseInt(chi.URLParam(r, "workflowID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid workflow ID")
		return
	}
	limit := clampLimit(queryInt(r, "limit", 20), 20, 200)
	offset := clampOffset(queryInt(r, "offset", 0))
	status := ptr.NonEmpty(r.URL.Query().Get("status"))

	runs, err := h.ws.ListRunsForWorkflow(r.Context(), workflowID, limit, offset, status)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

// ListRecentRuns returns recent runs across all workflows.
// GET /api/workflows/runs/recent
func (h *WorkflowHandler) ListRecentRuns(w http.ResponseWriter, r *http.Request) {
	limit := clampLimit(queryInt(r, "limit", 20), 20, 200)
	offset := clampOffset(queryInt(r, "offset", 0))
	status := ptr.NonEmpty(r.URL.Query().Get("status"))

	runs, err := h.ws.ListRecentRuns(r.Context(), limit, offset, status)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

// GetWorkflowRun returns a single run with step results.
// GET /api/workflows/runs/{runID}
func (h *WorkflowHandler) GetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	getByID(w, r, "runID", "invalid run ID", "run not found", h.ws.GetWorkflowRun)
}

// KillWorkflowRun kills a running workflow.
// POST /api/workflows/runs/{runID}/kill
func (h *WorkflowHandler) KillWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(chi.URLParam(r, "runID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid run ID")
		return
	}
	run, err := h.ws.GetWorkflowRunDirect(r.Context(), runID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if run == nil {
		errNotFound(w, "run not found")
		return
	}
	if run.Status != "pending" && run.Status != "running" {
		errConflict(w, "run is not active")
		return
	}

	// Use the runner for proper process/session cleanup if available
	if h.runner != nil {
		if h.runner.KillRun(r.Context(), runID) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run_id": runID, "status": "killed"})
			return
		}
	}

	// Fallback: mark killed in DB only
	errMsg := "killed via API"
	if err := h.ws.SetRunStatus(r.Context(), runID, "killed", &errMsg); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run_id": runID, "status": "killed"})
}

// ── Step Files ──────────────────────────────────────────────────────

// GetStepFile serves a file from a workflow step's directory.
// GET /api/workflows/runs/{runID}/steps/{stepIndex}/files/*
func (h *WorkflowHandler) GetStepFile(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(chi.URLParam(r, "runID"), 10, 64)
	if err != nil {
		errBadRequest(w, "invalid run ID")
		return
	}
	stepIndex, err := strconv.Atoi(chi.URLParam(r, "stepIndex"))
	if err != nil || stepIndex < 0 {
		errBadRequest(w, "invalid step index")
		return
	}

	run, err := h.ws.GetWorkflowRun(r.Context(), runID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if run == nil {
		errNotFound(w, "run not found")
		return
	}

	// Look up the workflow to get repo_path
	wf, err := h.ws.GetWorkflow(r.Context(), run.WorkflowID)
	if err != nil || wf == nil {
		errNotFound(w, "workflow not found")
		return
	}

	// Extract the file path from the wildcard
	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		errBadRequest(w, "file path required")
		return
	}

	// Build the absolute path and validate it's within the step directory
	stepDir := filepath.Join(h.cfg.CoralDir(), "workflows", "runs",
		strconv.FormatInt(runID, 10), fmt.Sprintf("step_%d", stepIndex))
	absPath := filepath.Join(stepDir, filepath.Clean(filePath))

	// Prevent path traversal — resolved path must be under stepDir
	if !strings.HasPrefix(absPath, stepDir+string(os.PathSeparator)) && absPath != stepDir {
		errBadRequest(w, "invalid file path")
		return
	}

	// A symlink inside a step directory must not lead out of it: compare the resolved paths.
	realStep, realFile := realPath(stepDir), realPath(absPath)
	if !within(realStep, realFile) {
		errNotFound(w, "file not found")
		return
	}
	// The run's own folder lives in the data folder, which sensitivePath walls off as a whole, so a local
	// request (the dashboard) is bounded by the step folder alone. A tunneled one is also refused names
	// sensitivePath lists (keys, credential files), judged by the path inside the step folder.
	if cloud.Tunneled(r) {
		rel, rerr := filepath.Rel(realStep, realFile)
		if rerr != nil || sensitivePath(filepath.VolumeName(realStep) + string(os.PathSeparator) + "step" + string(os.PathSeparator) + rel) {
			errNotFound(w, "file not found")
			return
		}
	}

	// Check file exists
	info, err := os.Stat(realFile)
	if err != nil || info.IsDir() {
		errNotFound(w, "file not found")
		return
	}
	if info.Size() > maxFileBytes()*20 {
		errPayloadTooLarge(w, "file too large")
		return
	}

	// A step (a shell command or an agent) wrote this file, under any name: an
	// .html or .svg must not run on the dashboard's origin. ServeFile keeps the
	// headers set here and only adds the length and ranges.
	setUntrustedContentHeaders(w.Header(), stepFileType(realFile), filepath.Base(realFile))
	http.ServeFile(w, r, realFile)
}

// canonicalizeRepoPath resolves symlinks and validates that the path is an existing directory.
// errNotADirectory is canonicalizeRepoPath's error for a path that exists but
// isn't a directory.
var errNotADirectory = errors.New("not a directory")

// repoPathError is the 400 message for a repo_path canonicalizeRepoPath refused.
func repoPathError(err error) string {
	if errors.Is(err, errNotADirectory) {
		return "repo_path is not a directory"
	}
	return "repo_path does not exist"
}

func canonicalizeRepoPath(p string) (string, error) {
	cleaned := filepath.Clean(p)
	resolved, err := safefs.Resolve(cleaned)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errNotADirectory
	}
	return resolved, nil
}

// ── Step Validation ──────────────────────────────────────────────────

// validateSteps validates step definitions per the spec rules.
func validateSteps(steps []json.RawMessage, defaultAgent json.RawMessage) string {
	stepNames := make(map[string]bool)
	if agentNamesMCP(defaultAgent) {
		return "default_agent: " + errMCPNotSupported
	}

	for i, stepRaw := range steps {
		var step struct {
			Name              string          `json:"name"`
			Type              string          `json:"type"`
			Command           string          `json:"command"`
			Prompt            string          `json:"prompt"`
			TimeoutS          *int            `json:"timeout_s"`
			ContinueOnFailure bool            `json:"continue_on_failure"`
			Agent             json.RawMessage `json:"agent"`
			Hooks             json.RawMessage `json:"hooks"`
			OutputArtifact    string          `json:"output_artifact"`
			Interactive       bool            `json:"interactive"`
		}
		if err := json.Unmarshal(stepRaw, &step); err != nil {
			return fmt.Sprintf("step %d: invalid JSON", i)
		}
		if agentNamesMCP(step.Agent) {
			return fmt.Sprintf("step %d: agent: %s", i, errMCPNotSupported)
		}
		// output_artifact stays inside the step's folder (#3423, CRIT-02).
		if step.OutputArtifact != "" && (!filepath.IsLocal(step.OutputArtifact) || strings.ContainsRune(step.OutputArtifact, 0)) {
			return fmt.Sprintf("step %d: output_artifact must be a relative path inside the step's folder (no absolute path, no ..)", i)
		}

		// Validate name
		if step.Name == "" {
			return fmt.Sprintf("step %d: name is required", i)
		}
		if stepNames[step.Name] {
			return "duplicate step name"
		}
		stepNames[step.Name] = true

		// Validate type
		switch step.Type {
		case "shell":
			if step.Command == "" {
				return "shell step missing command"
			}
		case "agent":
			if step.Prompt == "" {
				return "agent step missing prompt"
			}
			// Check agent_type is resolvable: the step's own, else the workflow's default.
			agentType := ""
			if step.Agent != nil {
				var agentCfg struct {
					AgentType string `json:"agent_type"`
				}
				json.Unmarshal(step.Agent, &agentCfg)
				agentType = agentCfg.AgentType
			}
			if agentType == "" && defaultAgent != nil {
				var daCfg struct {
					AgentType string `json:"agent_type"`
				}
				json.Unmarshal(defaultAgent, &daCfg)
				agentType = daCfg.AgentType
			}
			if agentType == "" {
				return "agent step missing agent_type"
			}
			// An unknown CLI is refused, and so is a print step on a CLI not verified for
			// it yet (#3522): a clear error now, not a silent failure or a guessed sandbox.
			if !background.KnownStepAgentType(agentType) {
				return fmt.Sprintf("step %d: unknown agent_type %q", i, agentType)
			}
			if !step.Interactive {
				if msg := background.PrintStepError(agentType); msg != "" {
					return fmt.Sprintf("step %d: %s", i, msg)
				}
			}
		default:
			return fmt.Sprintf("step %d: type must be 'shell' or 'agent'", i)
		}

		// Validate timeout_s
		if step.TimeoutS != nil && (*step.TimeoutS <= 0 || *step.TimeoutS > 86400) {
			return "invalid timeout"
		}

		// Validate template references
		text := step.Command + step.Prompt
		if errMsg := validateTemplateRefs(text, i, len(steps)); errMsg != "" {
			return errMsg
		}

		// Validate hooks if present
		if step.Hooks != nil {
			if errMsg := validateHooks(step.Hooks); errMsg != "" {
				return fmt.Sprintf("step %d: %s", i, errMsg)
			}
		}
	}
	return ""
}

// contentTemplate matches a template that inserts a previous step's output.
var contentTemplate = regexp.MustCompile(`\{\{\w+_content\}\}`)

// stepWarnings lists shell commands where a content template sits inside quotes (#3423):
// the runner inserts step output as one quoted shell word, so quoting it again puts the
// quotes into the value. Saving still works; the warnings come back with the workflow.
func stepWarnings(steps []json.RawMessage) []string {
	var out []string
	for i, raw := range steps {
		var step struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &step) != nil || step.Type != "shell" {
			continue
		}
		for _, loc := range contentTemplate.FindAllStringIndex(step.Command, -1) {
			if quoteAt(step.Command, loc[0]) != 0 {
				out = append(out, fmt.Sprintf("step %d (%s): %s is inside quotes; it is already inserted as one quoted shell word, so remove the quotes around it (or use \"$CORAL_PREV_STDOUT\")",
					i, step.Name, step.Command[loc[0]:loc[1]]))
			}
		}
	}
	return out
}

// quoteAt is the POSIX quote open at byte i of cmd: '\”, '"' or 0.
func quoteAt(cmd string, i int) byte {
	var q byte
	for j := 0; j < i && j < len(cmd); j++ {
		c := cmd[j]
		switch {
		case q == 0 && c == '\\':
			j++
		case q == '"' && c == '\\':
			j++
		case q == 0 && (c == '\'' || c == '"'):
			q = c
		case q != 0 && c == q:
			q = 0
		}
	}
	return q
}

// withWarnings adds "warnings" to a JSON object answer when there are any.
func withWarnings(v any, warnings []string) any {
	if len(warnings) == 0 {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	m["warnings"] = warnings
	return m
}

// validHookEvents are the recognized hook event names.
var validHookEvents = map[string]bool{
	"PreToolUse":   true,
	"PostToolUse":  true,
	"Stop":         true,
	"Notification": true,
	"SubagentStop": true,
	"StepComplete": true,
	"StepFailed":   true,
}

// validateHooks validates a hooks JSON object: known event names, structure, and limits.
func validateHooks(raw json.RawMessage) string {
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return "hooks must be a JSON object"
	}

	totalGroups := 0
	for event, groupsRaw := range hooks {
		if !validHookEvents[event] {
			return fmt.Sprintf("unknown hook event %q", event)
		}

		var groups []json.RawMessage
		if err := json.Unmarshal(groupsRaw, &groups); err != nil {
			return fmt.Sprintf("hooks.%s must be an array of hook groups", event)
		}

		if len(groups) > 10 {
			return fmt.Sprintf("hooks.%s exceeds max 10 groups", event)
		}
		totalGroups += len(groups)

		for j, groupRaw := range groups {
			var group struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(groupRaw, &group); err != nil {
				return fmt.Sprintf("hooks.%s[%d]: invalid hook group", event, j)
			}
			if len(group.Hooks) == 0 {
				return fmt.Sprintf("hooks.%s[%d]: hooks array is required", event, j)
			}
			for k, h := range group.Hooks {
				if h.Type != "command" {
					return fmt.Sprintf("hooks.%s[%d].hooks[%d]: type must be \"command\"", event, j, k)
				}
				if h.Command == "" {
					return fmt.Sprintf("hooks.%s[%d].hooks[%d]: command is required", event, j, k)
				}
			}
		}
	}

	if totalGroups > 50 {
		return "hooks exceed max 50 total groups"
	}

	return ""
}

// stepRefRe matches {{step_N_...}} template patterns and captures N.
var stepRefRe = regexp.MustCompile(`\{\{step_(\d+)_`)

// validateTemplateRefs checks template variable references in step text.
func validateTemplateRefs(text string, stepIndex, totalSteps int) string {
	// Check {{prev_*}} references on step 0
	if stepIndex == 0 {
		for _, tmpl := range []string{"{{prev_dir}}", "{{prev_stdout}}", "{{prev_stderr}}"} {
			if strings.Contains(text, tmpl) {
				return "step 0 cannot reference previous step"
			}
		}
	}

	// Check {{step_N_*}} references — N must be < current step index and within bounds
	matches := stepRefRe.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			return "template references invalid step index"
		}
		if n >= stepIndex || n >= totalSteps {
			return "template references invalid step index"
		}
	}

	return ""
}

// stepFileType is a step file's media type: from its extension, otherwise
// sniffed, with anything textual (including sniffed HTML) shown as plain text.
func stepFileType(path string) string {
	if t := mediatype.FromName(path); t != "" {
		return t
	}
	head := make([]byte, 512)
	f, err := os.Open(path)
	if err != nil {
		return mediatype.Generic
	}
	n, _ := io.ReadFull(f, head)
	f.Close()
	if t := http.DetectContentType(head[:n]); !strings.HasPrefix(t, "text/") {
		return t
	}
	return "text/plain; charset=utf-8"
}
