package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/safego"
)

// knowledgeJobs tracks distillation runs: the running one, or the last one to finish, per
// team. It lives in memory, so after a restart a team has none (an interrupted run is not
// reported as running forever) until it is started again (#3325).
var (
	knowledgeJobsMu sync.Mutex
	knowledgeJobs   = make(map[string]*knowledgeJob)
)

type knowledgeJob struct {
	TeamName string                     `json:"team_name"`
	Status   string                     `json:"status"`
	Progress background.DistillProgress `json:"progress"`
	Error    string                     `json:"error,omitempty"`
	// StartedAt / FinishedAt (RFC 3339, UTC): the dashboard shows when the knowledge was
	// last updated (#3325).
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	// A complete run can still be partial: the agents it could not distill (they keep
	// their earlier knowledge), and the cross-referencing pass's error.
	FailedAgents       []background.DistillFailure `json:"failed_agents,omitempty"`
	CollaborationError string                      `json:"collaboration_error,omitempty"`
}

// snapshot copies the job for writing out. Callers hold knowledgeJobsMu: the run's
// goroutine updates the job under it.
func (j *knowledgeJob) snapshot() knowledgeJob { return *j }

// knowledgeMinInterval is the least time between two runs for a team (#3518): a run spawns
// the Claude CLI once per member and spends model usage.
const knowledgeMinInterval = 60 * time.Second

// Bounds on what a status answer carries (#3518, coral-hub Security): it is shown through
// Coral Hub, so text is short, has no file paths, and lists a bounded number of agents.
const (
	knowledgeMaxText   = 200
	knowledgeMaxName   = 64
	knowledgeMaxFailed = 20
)

// A file path: absolute ("/home/x", "~/x", "./x"), or relative with two or more slashes or a
// file extension ("a/b/c", "notes/report.md"), so a word like "and/or" survives; a Windows
// drive or UNC path (C:\Users\..., \\server\share); and an email address, which the CLI's
// login and usage messages can carry (Security's and coral-hub Security's reviews).
var (
	knowledgePathRe    = regexp.MustCompile(`(^|[\s('"=])(?:[~.]*/[^\s'"),]*|[^\s'"(),=/]+(?:/[^\s'"(),=/]+){2,}/?|[^\s'"(),=/]+/[^\s'"(),=/]*\.[A-Za-z0-9]{1,8})`)
	knowledgeWinPathRe = regexp.MustCompile(`(?i)(^|[\s('"=])(?:[a-z]:[\\/]|\\\\)[^\s'"),]*`)
	knowledgeEmailRe   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// knowledgeText makes an error or name safe to show: one line, no file paths, bounded.
func knowledgeText(s string, max int) string {
	s = knowledgeEmailRe.ReplaceAllString(s, "<email>")
	s = knowledgeWinPathRe.ReplaceAllString(s, "${1}<path>")
	s = knowledgePathRe.ReplaceAllString(s, "${1}<path>")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// knowledgeStatus is the answer about a run: these keys only, text bounded (#3518).
type knowledgeStatus struct {
	Status             string                      `json:"status"`
	Progress           background.DistillProgress  `json:"progress"`
	Error              string                      `json:"error,omitempty"`
	StartedAt          string                      `json:"started_at,omitempty"`
	FinishedAt         string                      `json:"finished_at,omitempty"`
	FailedAgents       []background.DistillFailure `json:"failed_agents,omitempty"`
	CollaborationError string                      `json:"collaboration_error,omitempty"`
}

// knowledgeHubText replaces a run's error texts for a viewer through Coral Hub: scrubbing
// free text can't catch everything (a path with a space, a name), so a hub viewer gets this
// and the dashboard on the machine keeps the scrubbed detail (#3518, Security's review).
const knowledgeHubText = "distillation failed; see the dashboard on this machine"

// statusFor is the status for request r: through Coral Hub, the error texts are replaced.
func (j knowledgeJob) statusFor(r *http.Request) knowledgeStatus {
	out := j.status()
	if !cloud.Tunneled(r) {
		return out
	}
	fixed := func(s string) string {
		if s == "" {
			return ""
		}
		return knowledgeHubText
	}
	out.Error, out.CollaborationError, out.Progress.Error = fixed(out.Error), fixed(out.CollaborationError), fixed(out.Progress.Error)
	for i := range out.FailedAgents {
		out.FailedAgents[i].Error = fixed(out.FailedAgents[i].Error)
	}
	return out
}

func (j knowledgeJob) status() knowledgeStatus {
	out := knowledgeStatus{Status: j.Status, Progress: j.Progress, Error: knowledgeText(j.Error, knowledgeMaxText),
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, CollaborationError: knowledgeText(j.CollaborationError, knowledgeMaxText)}
	out.Progress.Agent = knowledgeText(out.Progress.Agent, knowledgeMaxName)
	out.Progress.Error = knowledgeText(out.Progress.Error, knowledgeMaxText)
	for i, f := range j.FailedAgents {
		if i == knowledgeMaxFailed {
			break
		}
		out.FailedAgents = append(out.FailedAgents, background.DistillFailure{Agent: knowledgeText(f.Agent, knowledgeMaxName), Error: knowledgeText(f.Error, knowledgeMaxText)})
	}
	return out
}

// teamExists reports whether name is a saved team with members or a board with live agents:
// a run is only started for a team that exists, so a made-up name never makes a job.
func (h *SessionsHandler) teamExists(ctx context.Context, name string) bool {
	if t, _ := h.teamStore.GetTeam(ctx, name); t != nil && len(t.Members) > 0 {
		return true
	}
	ls, err := h.ss.GetBoardSessions(ctx, name)
	return err == nil && len(ls) > 0
}

// DistillKnowledge starts or returns the status of a knowledge distillation run.
// POST /api/teams/detail/{name}/distill-knowledge
func (h *SessionsHandler) DistillKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	if !background.ValidKnowledgeTeam(teamName) {
		errBadRequest(w, "invalid team name")
		return
	}
	// No body and no query (#3518): the run is decided by the path alone.
	if b, _ := io.ReadAll(io.LimitReader(r.Body, 1024)); len(bytes.TrimSpace(b)) > 0 || r.URL.RawQuery != "" {
		errBadRequest(w, "this request takes no body or query")
		return
	}
	if h.teamStore == nil {
		errBadRequest(w, "team persistence not available")
		return
	}
	if !h.teamExists(r.Context(), teamName) {
		errNotFound(w, "team not found")
		return
	}

	knowledgeJobsMu.Lock()
	// One run per team: a second request while it runs gets that run, never a second one.
	if job, exists := knowledgeJobs[teamName]; exists && job.Status == "running" {
		snap := job.snapshot()
		knowledgeJobsMu.Unlock()
		writeJSON(w, http.StatusOK, snap.statusFor(r))
		return
	} else if exists {
		if started, err := time.Parse(time.RFC3339, job.StartedAt); err == nil {
			if wait := knowledgeMinInterval - time.Since(started); wait > 0 {
				knowledgeJobsMu.Unlock()
				secs := int(math.Ceil(wait.Seconds()))
				httputil.WriteActionLimitBody(w, secs, map[string]any{"error": "rate_limited",
					"message": fmt.Sprintf("a team's knowledge can be distilled once a minute; retry in %ds", secs)})
				return
			}
		}
	}

	job := &knowledgeJob{
		TeamName:  teamName,
		Status:    "running",
		Progress:  background.DistillProgress{Phase: "starting"},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	knowledgeJobs[teamName] = job
	started := job.snapshot()
	knowledgeJobsMu.Unlock()

	// Run distillation in background — use a detached context since the
	// HTTP request context is cancelled once the 202 response is sent.
	go func() {
		defer safego.Recover("server/routes/knowledge:198")
		progressFn := func(p background.DistillProgress) {
			knowledgeJobsMu.Lock()
			job.Progress = p
			knowledgeJobsMu.Unlock()
		}

		coralDir := h.cfg.CoralDir()
		ctx := context.Background()
		bundle, err := background.DistillTeamKnowledge(ctx, h.ss, h.teamStore, teamName, coralDir, progressFn)

		knowledgeJobsMu.Lock()
		job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if bundle != nil {
			job.FailedAgents, job.CollaborationError = bundle.FailedAgents, bundle.CollaborationError
		}
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			log.Printf("[knowledge] distillation failed for team %s: %v", teamName, err)
		} else {
			job.Status = "complete"
			log.Printf("[knowledge] distillation complete for team %s (%d agents failed)", teamName, len(job.FailedAgents))
		}
		knowledgeJobsMu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, started.statusFor(r))
}

// GetKnowledgeStatus returns the status of a distillation run.
// GET /api/teams/detail/{name}/distill-knowledge
func (h *SessionsHandler) GetKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	if !background.ValidKnowledgeTeam(teamName) {
		errBadRequest(w, "invalid team name")
		return
	}

	knowledgeJobsMu.Lock()
	job, exists := knowledgeJobs[teamName]
	var snap knowledgeJob
	if exists {
		snap = job.snapshot()
	}
	knowledgeJobsMu.Unlock()

	if !exists {
		writeJSON(w, http.StatusOK, map[string]string{"status": "none"})
		return
	}
	writeJSON(w, http.StatusOK, snap.statusFor(r))
}

// GetTeamKnowledge returns the distilled knowledge bundle for a team.
// GET /api/teams/detail/{name}/knowledge
func (h *SessionsHandler) GetTeamKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	coralDir := h.cfg.CoralDir()

	bundle, err := background.ReadKnowledgeBundle(coralDir, teamName)
	if errors.Is(err, background.ErrBadKnowledgeTeam) {
		errBadRequest(w, "invalid team name")
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if bundle == nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	// Build response with file contents
	type agentKnowledge struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}

	var agentFiles []agentKnowledge
	for name, concept := range bundle.Agents {
		agentFiles = append(agentFiles, agentKnowledge{
			Name:    name,
			Content: concept.FinalMD,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"exists":     true,
		"team_name":  bundle.TeamName,
		"created_at": bundle.CreatedAt,
		"index":      bundle.IndexMD,
		"agents":     agentFiles,
	})
}

// GetAgentKnowledge returns the knowledge file for a single agent.
// GET /api/teams/detail/{name}/knowledge/{agentName}
func (h *SessionsHandler) GetAgentKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	agentName := chi.URLParam(r, "agentName")
	coralDir := h.cfg.CoralDir()

	content := background.LoadAgentKnowledge(coralDir, teamName, agentName)
	if content == "" {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"exists":  true,
		"name":    agentName,
		"content": content,
	})
}

// SaveAgentKnowledge saves user edits to an agent's knowledge file.
// PUT /api/teams/detail/{name}/knowledge/{agentName}
func (h *SessionsHandler) SaveAgentKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	agentName := chi.URLParam(r, "agentName")
	coralDir := h.cfg.CoralDir()

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}

	// Load existing bundle to get the directory
	bundle, err := background.ReadKnowledgeBundle(coralDir, teamName)
	if err != nil || bundle == nil {
		errBadRequest(w, "no knowledge bundle exists for this team")
		return
	}

	// Update the agent's concept
	slug := background.Slugify(agentName)
	if concept, exists := bundle.Agents[slug]; exists {
		concept.FinalMD = body.Content
	} else {
		bundle.Agents[slug] = &background.AgentConcept{
			AgentName: agentName,
			FinalMD:   body.Content,
		}
	}

	// Rewrite to disk
	redacted, err := background.WriteKnowledgeFile(coralDir, teamName, agentName, body.Content)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// redacted: a secret in the text was replaced before it was saved (#4414).
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redacted": redacted})
}

// InjectKnowledgeIntoPrompt appends relevant knowledge to an agent's prompt.
func InjectKnowledgeIntoPrompt(coralDir, teamName, agentName, prompt string) string {
	knowledge := background.LoadAgentKnowledge(coralDir, teamName, agentName)
	if knowledge == "" {
		return prompt
	}

	// Also try loading by the team store's team name
	knowledgeSection := "\n\n---\n\n## Your Institutional Knowledge\n\n" +
		"The following is distilled knowledge from your previous sessions. " +
		"Use this to maintain continuity across restarts.\n\n" +
		knowledge

	if prompt == "" {
		return knowledgeSection
	}
	return prompt + knowledgeSection
}
