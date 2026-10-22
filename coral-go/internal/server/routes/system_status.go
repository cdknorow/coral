// Server status, the tmux checks and the defaults the dashboard asks for (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/firewall"
	"github.com/subgentic/coral/internal/tmux"
	"github.com/subgentic/coral/internal/tracking"
)

const defaultTmuxCacheTTL = 30 * time.Second

// wantsFreshStatus reports an explicit re-check: ?recheck=1, or a request that
// asks to bypass caches (fetch cache:"no-store" sends Cache-Control: no-cache).
func wantsFreshStatus(r *http.Request) bool {
	if v := r.URL.Query().Get("recheck"); v == "1" || v == "true" {
		return true
	}
	cc := strings.ToLower(r.Header.Get("Cache-Control"))
	return strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store")
}

// discoverTmux finds tmux for the status endpoint. This is discovery only (the
// file exists and is executable); it does not run tmux, so it says nothing
// about whether the executable works.
func (h *SystemHandler) discoverTmux(fresh bool) (string, bool) {
	ttl := h.tmuxCacheTTL
	if ttl == 0 {
		ttl = defaultTmuxCacheTTL
	}
	h.tmuxMu.Lock()
	defer h.tmuxMu.Unlock()
	if !fresh && !h.tmuxAt.IsZero() && time.Since(h.tmuxAt) < ttl {
		return h.tmuxPath, h.tmuxFound
	}
	h.tmuxPath, h.tmuxFound = tmuxProbe()
	h.tmuxAt = time.Now()
	return h.tmuxPath, h.tmuxFound
}

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
	// The operator's custom models (#5480) follow the curated ids of their provider's types, once each.
	custom, _ := h.loadCustomModels(r.Context()) // unreadable: the curated list alone
	out := make(map[string][]string, len(agenttypes.AgentTypeModels))
	for k, v := range agenttypes.AgentTypeModels {
		out[k] = agentmodels.MergeCustomValues(v, k, custom)
	}
	// Every agent type of a CLI family reads its family's list: the agy family is agy, antigravity and gemini (#5910), one CLI. A type with no curated entry of its own
	// (the antigravity alias) gets the family's main type's list, with the customs, so the launch dialog's picker is not empty for it.
	for k := range agenttypes.AgentTypeModels {
		types := agentmodels.FamilyTypes(agentmodels.Family(k))
		for _, t := range types {
			if _, ok := out[t]; !ok && len(types) > 0 {
				out[t] = append([]string{}, out[types[0]]...)
			}
		}
	}
	// A provider with no curated entry (Pi) still gets the customs added for it, under its main type.
	for _, c := range custom {
		if types := agentmodels.FamilyTypes(c.Family); len(types) > 0 {
			if _, ok := out[types[0]]; !ok {
				out[types[0]] = agentmodels.MergeCustomValues(nil, types[0], custom)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Status returns server status.
// GET /api/system/status
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	tmuxPath, tmuxAvailable := h.discoverTmux(wantsFreshStatus(r))
	// Analytics: observe tmux availability (deduplicated per run, so frequent
	// status polling sends at most one event per result).
	tmuxStatus := tracking.PrereqMissing
	if tmuxAvailable {
		tmuxStatus = tracking.PrereqAvailable
	}
	tracking.TrackPrerequisite(tracking.ToolTmux, tmuxStatus, tracking.SourceSystemStatus)
	out := map[string]any{
		"startup_complete":  true,
		"version":           config.Version,
		"tier_name":         config.TierName,
		"tmux_available":    tmuxAvailable,
		"tmux_status":       tmuxStatus,
		"tmux_server_issue": tmuxServerIssue(),
		"effective_backend": h.effectiveBackend(),
		// tmux_required: agents cannot be launched without tmux on this server
		// (it runs the tmux backend). false on the PTY backend.
		"tmux_required":        h.effectiveBackend() == "tmux",
		"tmux_path":            tmuxPath,
		"tmux_install_command": tmuxInstallCommand(),
		// A data folder other people on this computer can read (Windows; #3637): shown on the
		// dashboard's first page, not only in the log. "" when there is nothing to say.
		"security_warning": securityWarning(dataDirWarning(h.cfg.CoralDir()), auth.PeerCheckWarning()),
		// A Windows Firewall rule for Coral's programs that allows more than it needs (Public profile, every port), found by the
		// read-only startup check (#97): shown as its own warning with the commands that narrow or remove it. "" when none.
		"firewall_warning": firewall.Warning(),
		// Damage the integrity check found a minute after start (#4157); "" when none.
		"database_warning": background.DatabaseWarning(),
	}
	// Claude's sign-in as the last background check found it (#5469): present only once a check was made. false is the dashboard's one quiet line, "Claude is not signed in:
	// background jobs are waiting"; it clears itself once a check finds Claude signed in.
	if st, ok := agentsignin.Snapshot("claude"); ok {
		out["claude_signed_in"] = st.SignedIn
		out["claude_checked_at"] = st.CheckedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// securityWarning joins the security warnings that are set, for the dashboard's one banner.
func securityWarning(warnings ...string) string {
	var set []string
	for _, w := range warnings {
		if w != "" {
			set = append(set, w)
		}
	}
	return strings.Join(set, " ")
}

// dataDirWarning is config.DataDirWarning (a variable so a test can stand in for a Windows
// folder with a broad access list).
var dataDirWarning = config.DataDirWarning

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

// tmuxServerIssue is non-empty when the running tmux server cannot be talked
// to by this Coral's tmux (a client/server version mismatch). Coral never
// stops the old server; the message says how to recover.
func tmuxServerIssue() string {
	msg, _ := tmux.IncompatibleServer()
	return msg
}
