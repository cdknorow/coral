package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"gopkg.in/yaml.v3"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// Indexer is the interface for triggering an index refresh.
type Indexer interface {
	RunOnce(ctx context.Context) error
}

// SystemHandler handles settings, tags, and filesystem endpoints.
type SystemHandler struct {
	db      *store.DB
	ss      *store.SessionStore
	cfg     *config.Config
	indexer Indexer

	generateJobs   map[string]*generateJob
	generateJobsMu sync.Mutex
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(db *store.DB, cfg *config.Config) *SystemHandler {
	h := &SystemHandler{
		db:           db,
		ss:           store.NewSessionStore(db),
		cfg:          cfg,
		generateJobs: make(map[string]*generateJob),
	}
	// Cleanup expired generation jobs every minute
	go func() {
		for {
			time.Sleep(time.Minute)
			h.generateJobsMu.Lock()
			for id, job := range h.generateJobs {
				if time.Since(job.CreatedAt) > 10*time.Minute {
					delete(h.generateJobs, id)
				}
			}
			h.generateJobsMu.Unlock()
		}
	}()
	return h
}

// SetIndexer injects the session indexer for manual refresh triggers.
func (h *SystemHandler) SetIndexer(idx Indexer) {
	h.indexer = idx
}

// ── Settings ────────────────────────────────────────────────────────────

// sensitiveKeys are settings that should never be returned to the frontend.
var sensitiveKeys = map[string]bool{}

// GetSettings returns all user settings, filtering out sensitive keys.
// GET /api/settings
func (h *SystemHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryxContext(r.Context(), "SELECT key, value FROM user_settings")
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			if !sensitiveKeys[k] {
				settings[k] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

// PutSettings upserts one or more settings.
// PUT /api/settings
func (h *SystemHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	for k, v := range body {
		// Convert any value to string, matching Python's str() behavior.
		// Python's str(True)="True", str(False)="False" — capitalize bools.
		var s string
		switch val := v.(type) {
		case bool:
			if val {
				s = "True"
			} else {
				s = "False"
			}
		default:
			s = fmt.Sprintf("%v", v)
		}
		_, err := h.db.ExecContext(r.Context(),
			"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			k, s)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		// Apply select settings live so new WebSocket attaches see the change
		// without requiring a server restart.
		if k == "terminal_replay_bytes" {
			if n, err := strconv.Atoi(s); err == nil {
				ptymanager.SetReplayBytes(n)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Default Prompt Constants ─────────────────────────────────────────
// These reference the authoritative constants in the agent package so
// the frontend defaults stay in sync with the runtime prompts.

// GetDefaultPrompts returns the hardcoded default prompt templates.
// GET /api/settings/default-prompts
func (h *SystemHandler) GetDefaultPrompts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"default_prompt_orchestrator":        agent.DefaultOrchestratorActionPrompt,
		"default_prompt_worker":              agent.DefaultWorkerActionPrompt,
		"default_system_prompt_orchestrator": agent.DefaultOrchestratorSystemPrompt,
		"default_system_prompt_worker":       agent.DefaultWorkerSystemPrompt,
		"team_reminder_orchestrator":         "Remember to coordinate with your team and check the message board for updates",
		"team_reminder_worker":               "Remember to work with your team",
	})
}

// GetAgentModels returns the canonical per-agent-type model list used to
// populate the Model dropdown in the UI. The frontend still accepts free-form
// model strings — this list is only the curated suggestions.
// GET /api/agent-models
func (h *SystemHandler) GetAgentModels(w http.ResponseWriter, r *http.Request) {
	out := make(map[string][]string, len(agenttypes.AgentTypeModels))
	for k, v := range agenttypes.AgentTypeModels {
		if v == nil {
			out[k] = []string{}
		} else {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Status returns server status.
// GET /api/system/status
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	tmuxPath, tmuxAvailable := tmux.IsAvailable()
	writeJSON(w, http.StatusOK, map[string]any{
		"startup_complete":     true,
		"version":              config.Version,
		"store_url":            config.StoreURL,
		"supporter_urls":       SupporterCheckoutURLs(config.StoreURL),
		"skip_license":         config.TierSkipLicense,
		"tier_name":            config.TierName,
		"tmux_available":       tmuxAvailable,
		"tmux_path":            tmuxPath,
		"tmux_install_command": tmuxInstallCommand(),
	})
}

// tmuxInstallCommand returns the platform-appropriate install command for tmux.
func tmuxInstallCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install tmux"
	case "linux":
		return "sudo apt install tmux  (or your distro's package manager)"
	default:
		return "install tmux for your platform"
	}
}

var githubReleasesAPI = "https://api.github.com/repos/cdknorow/coral/releases/latest"

const githubReleasesURL = "https://github.com/cdknorow/coral/releases"

// FetchLatestVersion queries GitHub for the latest release version tag (without "v" prefix).
// Returns empty string on any error.
func FetchLatestVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", githubReleasesAPI, nil)
	if err != nil {
		return ""
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	return strings.TrimPrefix(data.TagName, "v")
}

// UpdateCheck returns update availability info.
// GET /api/system/update-check
func (h *SystemHandler) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	if config.TierSkipLicense {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"current":   config.Version,
		})
		return
	}
	latest := FetchLatestVersion()
	available := latest != "" && latest != config.Version && config.Version != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   available,
		"current":     config.Version,
		"latest":      latest,
		"release_url": githubReleasesURL,
	})
}

// CLICheck verifies whether a CLI tool is installed and returns its version.
// GET /api/system/cli-check?type=claude  (check by agent type)
// GET /api/system/cli-check?binary=/path/to/codex  (check specific path)
func (h *SystemHandler) CLICheck(w http.ResponseWriter, r *http.Request) {
	binaryPath := r.URL.Query().Get("binary")
	agentType := r.URL.Query().Get("type")

	if binaryPath == "" && agentType == "" {
		errBadRequest(w, "type or binary parameter required")
		return
	}

	var installCmd string
	if binaryPath == "" {
		info := agent.GetCLIInfo(agentType)
		if info == nil {
			writeJSON(w, http.StatusOK, map[string]any{"found": true, "agent_type": agentType})
			return
		}
		binaryPath = info.Binary
		installCmd = info.InstallCommand
	}

	// Try LookPath first, then common install locations
	resolvedPath, err := exec.LookPath(binaryPath)
	if err != nil {
		if found := agent.FindCLIInCommonPaths(binaryPath); found != "" {
			resolvedPath = found
		} else {
			result := map[string]any{
				"found":      false,
				"binary":     binaryPath,
				"agent_type": agentType,
			}
			if installCmd != "" {
				result["install_command"] = installCmd
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
	}

	// Try to get version
	version := ""
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, resolvedPath, "--version").Output(); err == nil {
		version = strings.TrimSpace(string(out))
		// Take first line only
		if idx := strings.IndexByte(version, '\n'); idx > 0 {
			version = version[:idx]
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"found":      true,
		"path":       resolvedPath,
		"version":    version,
		"agent_type": agentType,
	})
}

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

// ListFilesystem lists directories for the directory browser.
// GET /api/filesystem/list?path=~
func (h *SystemHandler) ListFilesystem(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "~"
	}

	// Expand ~ to home directory
	if strings.HasPrefix(reqPath, "~") {
		home, _ := os.UserHomeDir()
		reqPath = filepath.Join(home, reqPath[1:])
	}

	expanded, err := filepath.Abs(reqPath)
	if err != nil {
		errBadRequest(w, "invalid path")
		return
	}

	// Security: restrict to home directory
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(expanded, home) {
		errForbidden(w, "access denied")
		return
	}

	entries, err := os.ReadDir(expanded)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    expanded,
			"entries": []any{},
			"error":   err.Error(),
		})
		return
	}

	// Python returns flat string array of directory names (not objects)
	dirs := make([]string, 0)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden
		}
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	writeJSON(w, http.StatusOK, map[string]any{
		"path":    expanded,
		"entries": dirs,
	})
}

// IsGitDir checks if a directory is a git repository.
// GET /api/filesystem/is-git?path=/some/dir
func (h *SystemHandler) IsGitDir(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	if strings.HasPrefix(reqPath, "~") {
		home, _ := os.UserHomeDir()
		reqPath = filepath.Join(home, reqPath[1:])
	}
	expanded, err := filepath.Abs(reqPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	_, err = os.Stat(filepath.Join(expanded, ".git"))
	writeJSON(w, http.StatusOK, map[string]bool{"is_git": err == nil})
}

// ── Tags ────────────────────────────────────────────────────────────────

// ListTags returns all tags.
// GET /api/tags
func (h *SystemHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	type tag struct {
		ID    int    `json:"id" db:"id"`
		Name  string `json:"name" db:"name"`
		Color string `json:"color" db:"color"`
	}
	var tags []tag
	if err := h.db.SelectContext(r.Context(), &tags, "SELECT id, name, color FROM tags ORDER BY name"); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// CreateTag creates a new tag.
// POST /api/tags
func (h *SystemHandler) CreateTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		errBadRequest(w, "Tag name is required")
		return
	}
	if body.Color == "" {
		body.Color = "#58a6ff"
	}
	result, err := h.db.ExecContext(r.Context(),
		"INSERT INTO tags (name, color) VALUES (?, ?)", body.Name, body.Color)
	if err != nil {
		errConflict(w, "tag already exists")
		return
	}
	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": body.Name, "color": body.Color})
}

// DeleteTag removes a tag.
// DELETE /api/tags/{tagID}
func (h *SystemHandler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(), "DELETE FROM tags WHERE id = ?", tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AddSessionTag adds a tag to a session.
// POST /api/sessions/history/{sessionID}/tags
func (h *SystemHandler) AddSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	var body struct {
		TagID int `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	h.db.ExecContext(r.Context(),
		"INSERT OR IGNORE INTO session_tags (session_id, tag_id) VALUES (?, ?)",
		sessionID, body.TagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveSessionTag removes a tag from a session.
// DELETE /api/sessions/history/{sessionID}/tags/{tagID}
func (h *SystemHandler) RemoveSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(),
		"DELETE FROM session_tags WHERE session_id = ? AND tag_id = ?",
		sessionID, tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Folder Tags ─────────────────────────────────────────────────────────

// GetAllFolderTags returns all folder tags grouped by folder name.
// GET /api/folder-tags
func (h *SystemHandler) GetAllFolderTags(w http.ResponseWriter, r *http.Request) {
	result, err := h.ss.GetAllFolderTags(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if result == nil {
		result = map[string][]store.Tag{}
	}
	writeJSON(w, http.StatusOK, result)
}

// GetFolderTags returns tags for a specific folder.
// GET /api/folder-tags/{folderName}
func (h *SystemHandler) GetFolderTags(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tags, err := h.ss.GetFolderTags(r.Context(), folderName)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// AddFolderTag adds a tag to a folder.
// POST /api/folder-tags/{folderName}
func (h *SystemHandler) AddFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	var body struct {
		TagID int64 `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	if err := h.ss.AddFolderTag(r.Context(), folderName, body.TagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveFolderTag removes a tag from a folder.
// DELETE /api/folder-tags/{folderName}/{tagID}
func (h *SystemHandler) RemoveFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tagID"), 10, 64)
	if err := h.ss.RemoveFolderTag(r.Context(), folderName, tagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// QRCode generates a QR code PNG for the given URL.
// GET /api/system/qr?url=<encoded_url>
func (h *SystemHandler) QRCode(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(png)
}

// NetworkInfo returns the server's LAN IP addresses and port.
// GET /api/system/network-info
func (h *SystemHandler) NetworkInfo(w http.ResponseWriter, r *http.Request) {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	primary := ""
	for _, ip := range ips {
		if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.") {
			primary = ip
			break
		}
	}
	if primary == "" && len(ips) > 0 {
		primary = ips[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ips":     ips,
		"primary": primary,
		"port":    h.cfg.Port,
	})
}

// ── Team Import ─────────────────────────────────────────────────────────

// ImportTeam parses a folder-based team definition and returns team config JSON.
// POST /api/teams/import
//
// The folder structure is:
//
//	my-team/
//	  SKILL.md         → Orchestrator agent (frontmatter: name, description, tools, mcp_servers)
//	  agents/
//	    agent-name.md  → Worker agents (frontmatter: name, description, tools, mcp_servers)
//
// Each .md file has YAML frontmatter (---\nkey: value\n---) followed by the prompt body.
func (h *SystemHandler) ImportTeam(w http.ResponseWriter, r *http.Request) {
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

	info, err := os.Stat(body.Path)
	if err != nil || !info.IsDir() {
		errBadRequest(w, "path does not exist or is not a directory")
		return
	}

	teamName := filepath.Base(body.Path)
	var agents []map[string]any

	// Parse SKILL.md as orchestrator
	skillPath := filepath.Join(body.Path, "SKILL.md")
	if data, err := os.ReadFile(skillPath); err == nil {
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
		if len(meta.MCPServers) > 0 {
			agentDef["mcpServers"] = meta.MCPServers
		}
		agents = append(agents, agentDef)
	}

	// Parse agents/*.md as workers
	agentsDir := filepath.Join(body.Path, "agents")
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(agentsDir, entry.Name()))
			if err != nil {
				continue
			}
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
			if len(meta.MCPServers) > 0 {
				agentDef["mcpServers"] = meta.MCPServers
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

// ── Team Generation (async) ─────────────────────────────────────────

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

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Claude CLI not found — install Claude Code (npm install -g @anthropic-ai/claude-code)",
		})
		return
	}

	jobID := "gen-" + uuid.New().String()
	job := &generateJob{Status: "pending", CreatedAt: time.Now()}

	h.generateJobsMu.Lock()
	h.generateJobs[jobID] = job
	h.generateJobsMu.Unlock()

	prompt := teamGeneratePrompt +
		"\n\n<inputs>" +
		"\n<directive>\n" + body.Directive + "\n</directive>" +
		"\n<composition>\n" + body.Composition + "\n</composition>" +
		"\n</inputs>"

	coralDir := h.cfg.CoralDir()

	go func() {
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
	job, ok := h.generateJobs[jobID]
	h.generateJobsMu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
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
	cmd := executil.Command(ctx, claudePath,
		"--print",
		"--model", "opus",
		"--no-session-persistence",
		prompt,
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, "Claude CLI failed: " + err.Error()
	}

	// Write raw response to ~/.coral/last_generated_team.json for debugging
	if coralDir != "" {
		_ = os.WriteFile(filepath.Join(coralDir, "last_generated_team.json"), output, 0644)
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
		if servers, ok := ag["mcpServers"]; !ok || servers == nil {
			ag["mcpServers"] = map[string]any{}
		} else if _, ok := servers.(map[string]any); !ok {
			return nil, fmt.Sprintf("agent '%s' has invalid mcpServers; expected object", name)
		}

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
      "mcpServers": {
        "github": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-github"],
          "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" }
        }
      },
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
8. If useful, agent objects MAY also include "tools" (array of strings) and "mcpServers" (object keyed by server name).
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
Only include "tools" and "mcpServers" when they materially help the role. Do not invent MCP servers unless the request implies a real integration.

Few-shot example:
{
  "name": "Coding Team",
  "agents": [
    {
      "name": "Orchestrator",
      "prompt": "You are the orchestrator for a coding team. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Break the work into steps, discuss your plan with the operator before posting assignments, delegate implementation and verification to the team via the message board, and track progress. Do not do the implementation work yourself.",
      "tools": [],
      "mcpServers": {},
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
      "mcpServers": {},
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
      "mcpServers": {},
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
- If present, ensure "tools" is an array of strings and "mcpServers" is an object.
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
	MCPServers  map[string]any `yaml:"mcp_servers"`
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
