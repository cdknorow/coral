package routes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/background"
)

// knowledgeJobs tracks in-progress distillation runs.
var (
	knowledgeJobsMu sync.Mutex
	knowledgeJobs   = make(map[string]*knowledgeJob)
)

type knowledgeJob struct {
	TeamName string                     `json:"team_name"`
	Status   string                     `json:"status"`
	Progress background.DistillProgress `json:"progress"`
	Error    string                     `json:"error,omitempty"`
}

// DistillKnowledge starts or returns the status of a knowledge distillation run.
// POST /api/teams/detail/{name}/distill-knowledge
func (h *SessionsHandler) DistillKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	if teamName == "" {
		errBadRequest(w, "team name required")
		return
	}

	if h.teamStore == nil {
		errBadRequest(w, "team persistence not available")
		return
	}

	knowledgeJobsMu.Lock()
	if job, exists := knowledgeJobs[teamName]; exists && job.Status == "running" {
		knowledgeJobsMu.Unlock()
		writeJSON(w, http.StatusOK, job)
		return
	}

	job := &knowledgeJob{
		TeamName: teamName,
		Status:   "running",
		Progress: background.DistillProgress{Phase: "starting"},
	}
	knowledgeJobs[teamName] = job
	knowledgeJobsMu.Unlock()

	// Run distillation in background — use a detached context since the
	// HTTP request context is cancelled once the 202 response is sent.
	go func() {
		progressFn := func(p background.DistillProgress) {
			knowledgeJobsMu.Lock()
			job.Progress = p
			knowledgeJobsMu.Unlock()
		}

		coralDir := h.cfg.CoralDir()
		ctx := context.Background()
		_, err := background.DistillTeamKnowledge(ctx, h.ss, h.teamStore, teamName, coralDir, progressFn)

		knowledgeJobsMu.Lock()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			log.Printf("[knowledge] distillation failed for team %s: %v", teamName, err)
		} else {
			job.Status = "complete"
			log.Printf("[knowledge] distillation complete for team %s", teamName)
		}
		knowledgeJobsMu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, job)
}

// GetKnowledgeStatus returns the status of a distillation run.
// GET /api/teams/detail/{name}/distill-knowledge
func (h *SessionsHandler) GetKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")

	knowledgeJobsMu.Lock()
	job, exists := knowledgeJobs[teamName]
	knowledgeJobsMu.Unlock()

	if !exists {
		writeJSON(w, http.StatusOK, map[string]string{"status": "none"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// GetTeamKnowledge returns the distilled knowledge bundle for a team.
// GET /api/teams/detail/{name}/knowledge
func (h *SessionsHandler) GetTeamKnowledge(w http.ResponseWriter, r *http.Request) {
	teamName := chi.URLParam(r, "name")
	coralDir := h.cfg.CoralDir()

	bundle, err := background.ReadKnowledgeBundle(coralDir, teamName)
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
	if err := background.WriteKnowledgeFile(coralDir, teamName, agentName, body.Content); err != nil {
		errInternalServer(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

