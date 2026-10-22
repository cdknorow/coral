// Importing a team and generating one with the agent CLI (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/safego"
	"gopkg.in/yaml.v3"
)

// generateJobTTL is how long a team-generation job's result is kept.
const generateJobTTL = 10 * time.Minute

// At most this many team generations run at once (each is a CLI process), and this many job records are kept (#4602).
const (
	maxGenerateRunning = 2
	maxGenerateJobs    = 50
)

// pruneGenerateJobsLocked drops the jobs older than generateJobTTL, when a job is added or asked
// for. A goroutine per SystemHandler swept them every minute and never ended (#4167, goleak).
func (h *SystemHandler) pruneGenerateJobsLocked() {
	for id, job := range h.generateJobs {
		if time.Since(job.CreatedAt) > generateJobTTL {
			delete(h.generateJobs, id)
		}
	}
}

// ImportTeam parses a folder-based team definition and returns team config JSON.
// POST /api/teams/import
//
// The folder structure is:
//
//	my-team/
//	  SKILL.md         → Orchestrator agent (frontmatter: name, description, tools)
//	  agents/
//	    agent-name.md  → Worker agents (frontmatter: name, description, tools)
//
// A file whose frontmatter has mcp_servers is refused: Coral doesn't configure MCP servers (#3644).
//
// Each .md file has YAML frontmatter (---\nkey: value\n---) followed by the prompt body.
func (h *SystemHandler) ImportTeam(w http.ResponseWriter, r *http.Request) {
	// Reading a folder of the caller's choosing is the Operator's (#3583, Security's sweep item 4):
	// an agent's token is refused, and the folder and its files are bounded (importTeamRoot,
	// readImportFile): no symlink is followed, no dot-file or sensitive path is read, files are
	// capped at 256 KiB and agents/ at 50.
	if !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can import a team from a folder")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Path == "" {
		errBadRequest(w, "path is required")
		return
	}

	root, err := importTeamRoot(body.Path)
	if err != nil {
		errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
		return
	}

	teamName := filepath.Base(body.Path)
	var agents []map[string]any

	// Parse SKILL.md as orchestrator
	skillPath := filepath.Join(root, "SKILL.md")
	data, found, err := readImportFile(root, skillPath)
	if err != nil {
		errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
		return
	}
	if found {
		meta, prompt := parseFrontmatterMD(string(data))
		if meta.Name == "" {
			meta.Name = "Orchestrator"
		}
		agentDef := map[string]any{
			"name":   meta.Name,
			"role":   "orchestrator",
			"prompt": prompt,
		}
		if meta.Description != "" {
			agentDef["description"] = meta.Description
		}
		if len(meta.Tools) > 0 {
			agentDef["tools"] = meta.Tools
		}
		if meta.MCPServers != nil {
			errBadRequest(w, "SKILL.md: mcp_servers is not supported: configure MCP servers in the agent CLI's own settings")
			return
		}
		agents = append(agents, agentDef)
	}

	// Parse agents/*.md as workers
	agentsDir := filepath.Join(root, "agents")
	if fi, lerr := os.Lstat(agentsDir); lerr == nil && fi.IsDir() {
		entries, _ := os.ReadDir(agentsDir)
		read := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			if read >= importAgentsMax {
				errBadRequest(w, fmt.Sprintf("agents/ has more than %d agent files", importAgentsMax))
				return
			}
			data, found, err := readImportFile(root, filepath.Join(agentsDir, entry.Name()))
			if err != nil {
				errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
				return
			}
			if !found {
				continue
			}
			read++
			meta, prompt := parseFrontmatterMD(string(data))
			if meta.Name == "" {
				meta.Name = strings.TrimSuffix(entry.Name(), ".md")
			}
			agentDef := map[string]any{
				"name":   meta.Name,
				"prompt": prompt,
			}
			if meta.Description != "" {
				agentDef["description"] = meta.Description
			}
			if len(meta.Tools) > 0 {
				agentDef["tools"] = meta.Tools
			}
			if meta.MCPServers != nil {
				errBadRequest(w, "agents/"+entry.Name()+": mcp_servers is not supported: configure MCP servers in the agent CLI's own settings")
				return
			}
			agents = append(agents, agentDef)
		}
	}

	if len(agents) == 0 {
		errBadRequest(w, "no agent definitions found in directory")
		return
	}

	teamConfig := map[string]any{
		"name":   teamName,
		"agents": agents,
	}

	writeJSON(w, http.StatusOK, teamConfig)
}

type generateJob struct {
	Status    string         `json:"status"` // "pending", "complete", "error"
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"-"`
}

// GenerateTeam kicks off an async Claude CLI call and returns a job ID.
// POST /api/teams/generate
func (h *SystemHandler) GenerateTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Directive   string `json:"directive"`
		Composition string `json:"composition"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Directive) == "" {
		errBadRequest(w, "directive is required")
		return
	}

	const maxInputLen = 4000
	if len(body.Directive) > maxInputLen {
		errBadRequest(w, "directive too long (max 4000 characters)")
		return
	}
	if len(body.Composition) > maxInputLen {
		errBadRequest(w, "composition too long (max 4000 characters)")
		return
	}

	settings, serr := h.ss.GetSettingsForCLI(r.Context())
	if serr != nil {
		writeError( // never run the CLI found on PATH instead of the configured one (#4350)
			w, http.StatusServiceUnavailable, "", "Coral's settings could not be read; try again")
		return
	}
	claudePath, err := agent.FindCLI(settings, agenttypes.Claude)
	if err != nil {
		msg := "Claude CLI not found — install Claude Code (npm install -g @anthropic-ai/claude-code) or set its path in Settings > CLI paths"
		if agent.ResolveCLIPath(settings, agenttypes.Claude) != "" {
			msg = err.Error() // a configured path that can't be run says so (#4432)
		}
		writeError(w, http.StatusServiceUnavailable, "", msg)
		return
	}

	jobID := "gen-" + uuid.New().String()
	job := &generateJob{Status: "pending", CreatedAt: time.Now()}

	h.generateJobsMu.Lock()
	h.pruneGenerateJobsLocked()
	// Each job is a `claude` process of up to 120 s (and API spend): at most maxGenerateRunning at once, and the finished results kept
	// are bounded too (#4602).
	running := 0
	for _, j := range h.generateJobs {
		if j.Status == "pending" {
			running++
		}
	}
	if running >= maxGenerateRunning {
		h.generateJobsMu.Unlock()
		// A limit on this action, not an overloaded Coral: the marker keeps the dashboard's overload watch from pausing its pollers.
		httputil.WriteActionLimit(w, 10, "busy", "Teams are already being generated: try again in a moment.")
		return
	}
	for len(h.generateJobs) >= maxGenerateJobs {
		oldest := ""
		for id, j := range h.generateJobs {
			if j.Status != "pending" && (oldest == "" || j.CreatedAt.Before(h.generateJobs[oldest].CreatedAt)) {
				oldest = id
			}
		}
		if oldest == "" {
			break
		}
		delete(h.generateJobs, oldest)
	}
	h.generateJobs[jobID] = job
	h.generateJobsMu.Unlock()

	prompt := teamGeneratePrompt +
		"\n\n<inputs>" +
		"\n<directive>\n" + body.Directive + "\n</directive>" +
		"\n<composition>\n" + body.Composition + "\n</composition>" +
		"\n</inputs>"

	coralDir := h.cfg.CoralDir()

	go func() {
		defer safego.Recover("server/routes/system:1327")
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, errMsg := runTeamGeneration(ctx, claudePath, prompt, coralDir)

		h.generateJobsMu.Lock()
		defer h.generateJobsMu.Unlock()
		if errMsg != "" {
			job.Status = "error"
			job.Error = errMsg
		} else {
			job.Status = "complete"
			job.Result = result
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "pending"})
}

// GenerateTeamStatus returns the status of an async team generation job.
// GET /api/teams/generate/{jobId}
func (h *SystemHandler) GenerateTeamStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobId")

	h.generateJobsMu.Lock()
	h.pruneGenerateJobsLocked()
	job, ok := h.generateJobs[jobID]
	h.generateJobsMu.Unlock()

	if !ok {
		errNotFound(w, "job not found")
		return
	}

	resp := map[string]any{"job_id": jobID, "status": job.Status}
	if job.Status == "complete" {
		resp["result"] = job.Result
	}
	if job.Status == "error" {
		resp["error"] = job.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

// runTeamGeneration executes Claude CLI and parses/validates the response.
// Returns (result, errorMessage). If errorMessage is non-empty, result is nil.
func runTeamGeneration(ctx context.Context, claudePath, prompt, coralDir string) (map[string]any, string) {
	// Signed out: the job ends at once with that sentence and Claude's sign-in flow is never started (#5469). Fresh, so a sign-in that just happened is seen.
	if err := agentsignin.RequireFresh(ctx, "claude", claudePath); err != nil {
		return nil, err.Error()
	}
	cmd := executil.Command(ctx, claudePath,
		"--print",
		"--model", "opus",
		"--no-session-persistence",
		prompt,
	)
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
	output, err := cmd.Output()
	if err != nil {
		return nil, "Claude CLI failed: " + err.Error()
	}

	// Write raw response to ~/.coral/last_generated_team.json for debugging
	if coralDir != "" {
		_ = os.WriteFile(filepath.Join(coralDir, "last_generated_team.json"), output, 0o600)
	}

	// Strip markdown fences if present
	raw := strings.TrimSpace(string(output))
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var cleaned []string
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "```") {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.TrimSpace(strings.Join(cleaned, "\n"))
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, "Failed to parse LLM response as JSON"
	}

	// Validate that agents array exists
	agents, ok := result["agents"].([]any)
	if !ok || len(agents) == 0 {
		return nil, "Generated team has no agents"
	}

	// Validate and normalize each agent — fill defaults for missing fields
	for i, a := range agents {
		ag, ok := a.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("agent at index %d is not a valid object", i)
		}

		name, _ := ag["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Sprintf("agent at index %d is missing a name", i)
		}

		agPrompt, _ := ag["prompt"].(string)
		if strings.TrimSpace(agPrompt) == "" {
			return nil, fmt.Sprintf("agent '%s' is missing a prompt", name)
		}

		if at, _ := ag["agent_type"].(string); at == "" {
			ag["agent_type"] = "claude"
		}
		if _, exists := ag["model"]; !exists {
			ag["model"] = ""
		}
		if tools, ok := ag["tools"]; !ok || tools == nil {
			ag["tools"] = []any{}
		} else if _, ok := tools.([]any); !ok {
			return nil, fmt.Sprintf("agent '%s' has invalid tools; expected array", name)
		}
		// Coral doesn't configure MCP servers (#3644): a generated team never carries any.
		delete(ag, "mcpServers")

		caps, _ := ag["capabilities"].(map[string]any)
		if caps == nil {
			caps = map[string]any{"allow": []any{"file_read"}, "deny": []any{}}
			ag["capabilities"] = caps
		} else {
			if _, ok := caps["allow"]; !ok {
				caps["allow"] = []any{"file_read"}
			}
			if _, ok := caps["deny"]; !ok {
				caps["deny"] = []any{}
			}
		}

		agents[i] = ag
	}
	result["agents"] = agents

	if name, _ := result["name"].(string); strings.TrimSpace(name) == "" {
		result["name"] = "Generated Team"
	}
	if _, exists := result["flags"]; !exists {
		result["flags"] = ""
	}

	return result, ""
}

const teamGeneratePrompt = `Role and task:
You generate team configuration JSON for Coral, an AI agent orchestration platform. Read the user's directive and composition guidance, then produce a single valid team configuration object.

Output contract:
You MUST respond with ONLY one valid JSON object.
Do not include markdown, code fences, comments, trailing commas, or explanatory text.
Every string must be valid escaped JSON.
The response must match this exact schema:

{
  "name": "Short team name (2-4 words)",
  "agents": [
    {
      "name": "Agent Display Name",
      "prompt": "Detailed prompt for this agent",
      "tools": ["TodoWrite", "Bash(npm *)"],
      "capabilities": {
        "allow": ["capability1", "capability2"],
        "deny": ["capability3"]
      },
      "agent_type": "claude",
      "model": ""
    }
  ],
  "flags": ""
}

Hard platform rules:
1. The first agent MUST be named "Orchestrator".
2. Every team MUST contain 3-8 agents. If the user asks for fewer than 3, still produce 3 agents by adding the minimum supporting roles needed. If the user asks for more than 8, consolidate responsibilities to stay within 8.
3. The Orchestrator coordinates work, delegates tasks via the message board, tracks progress, and should not do the implementation work itself.
4. The Orchestrator must include these allowed capabilities: ["file_read", "shell:coral-board *", "agent_spawn", "web_access"].
5. Every worker prompt must say they are automatically joined to the message board, must not run "coral-board join", must not poll or loop on "coral-board read", and must wait for instructions from the Orchestrator before starting work.
6. The Orchestrator prompt must say it is automatically joined to the message board, must not run "coral-board join", must not poll or loop on "coral-board read", and should discuss its plan with the operator before posting assignments.
7. Every agent object MUST include all of these keys: "name", "prompt", "capabilities", "agent_type", and "model".
8. If useful, agent objects MAY also include "tools" (array of strings). Never include "mcpServers": Coral does not configure MCP servers.
9. Every "capabilities" object MUST include both "allow" and "deny" arrays, even if "deny" is empty.
10. Use "claude" for "agent_type" unless the user explicitly requests a different agent type.
11. Use an empty string for "model" unless the user explicitly requests a specific model for that agent.
12. Use an empty string for "flags" unless the user explicitly requests flags.

Capability policy:
Use only these capabilities:
- file_read: Read files (Read, Glob, Grep tools)
- file_write: Write/edit files (Write, Edit tools)
- shell: Execute shell commands (Bash tool)
- web_access: Web browsing (WebFetch, WebSearch tools)
- git_write: Git operations (push, commit, branch, merge, rebase)
- agent_spawn: Launch sub-agents
- notebook: Edit Jupyter notebooks
- shell:<pattern>: Restricted shell (for example "shell:npm *" or "shell:coral-board *")

Assign the minimum capabilities each agent needs. Follow least privilege.
If a task can be done without shell access, do not grant shell.
Prefer restricted shell patterns over unrestricted "shell" when practical.
If the user requests unknown or unsupported capabilities, ignore those requests and use the closest supported safe alternative.

Input interpretation rules:
Treat <directive> as the mission and desired outcome.
Treat <composition> as preferred team structure, role mix, skill emphasis, model preferences, and constraints.
If <composition> is empty or vague, infer a reasonable team structure from <directive>.
If the user's requests conflict with the hard platform rules, obey the hard platform rules.
If the user asks for an overpowered agent, reduce permissions to the minimum needed.
In prompts, make each role specific and concrete. Avoid generic filler.
Only include "tools" when they materially help the role.

Few-shot example:
{
  "name": "Coding Team",
  "agents": [
    {
      "name": "Orchestrator",
      "prompt": "You are the orchestrator for a coding team. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Break the work into steps, discuss your plan with the operator before posting assignments, delegate implementation and verification to the team via the message board, and track progress. Do not do the implementation work yourself.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    },
    {
      "name": "Lead Developer",
      "prompt": "You are the lead developer. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Wait for instructions from the Orchestrator before starting. Implement features, modify code, run necessary development commands, and coordinate status updates through the message board.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    },
    {
      "name": "QA Engineer",
      "prompt": "You are the QA engineer. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Wait for instructions from the Orchestrator before starting. Review changes, write or run tests when needed, verify behavior, and report risks and regressions through the message board.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    }
  ],
  "flags": ""
}

Final validation checklist:
- Output exactly one JSON object and nothing else.
- Ensure the first agent is "Orchestrator".
- Ensure there are 3-8 agents.
- Ensure every agent has "name", "prompt", "capabilities", "agent_type", and "model".
- If present, ensure "tools" is an array of strings, and that no agent has "mcpServers".
- Ensure every capabilities object has both "allow" and "deny".
- Ensure every capability string is from the supported list above.
- Ensure prompts include the message board coordination rules.
- Ensure "flags" is present.

Delimited user inputs:
You will receive:
- <directive>...</directive>
- <composition>...</composition>

Generate the team configuration now.`

type agentFrontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Tools       []string       `yaml:"tools"`
	MCPServers  map[string]any `yaml:"mcp_servers"` // only to refuse it (#3644)
}

// parseFrontmatterMD extracts YAML frontmatter and body from markdown.
// Returns (frontmatter, body). If no frontmatter, body is the full content.
func parseFrontmatterMD(content string) (agentFrontmatter, string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return agentFrontmatter{}, content
	}

	// Find closing ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return agentFrontmatter{}, content
	}

	frontmatter := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])

	var meta agentFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return agentFrontmatter{}, content
	}

	return meta, body
}
