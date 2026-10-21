// Checking that the agent CLIs are installed and their versions (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"errors"
	"github.com/subgentic/coral/internal/agentenv"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/tmux"
	"github.com/subgentic/coral/internal/tracking"
)

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
	if binaryPath != "" {
		// Running a named binary is the Operator's, and only a CLI's: never a path an agent
		// chose or could have written (#3583, Security's sweep item 5).
		if cloud.Tunneled(r) || !humanInputCaller(r) {
			writeError(w, http.StatusForbidden, "operator_only", "only the Operator can check a CLI by its path")
			return
		}
		if why := h.cliCheckRefusal(r.Context(), binaryPath, agentType); why != "" {
			errBadRequest(w, why)
			return
		}
	}

	var installCmd, installURL, installNote string
	if binaryPath == "" {
		info := agent.GetCLIInfo(agentType)
		if info == nil {
			writeJSON(w, http.StatusOK, map[string]any{"found": true, "agent_type": agentType})
			return
		}
		binaryPath = info.Binary
		installCmd, installURL, installNote = info.InstallCommand, info.InstallURL, info.InstallNote
		// The CLI path setting is what launch runs, so a check by type looks
		// there first: a configured path is not reported as "not found" (#3169).
		// It is never replaced by the CLI on PATH (#4354): this check RUNS what it finds
		// (--version), so unreadable settings or a configured path that cannot be run is
		// said so, as launch and the model listing say it (#4350, #4432).
		if h.ss != nil {
			settings, serr := h.ss.GetSettingsForCLI(r.Context())
			if serr != nil {
				writeError(w, http.StatusServiceUnavailable, "", "Coral's settings could not be read; try again")
				return
			}
			if p := agent.ResolveCLIPath(settings, agentType); p != "" {
				if _, err := cliLookPath(p); err != nil {
					writeJSON(w, http.StatusOK, hubCLIReply(r, map[string]any{
						"found":      false,
						"status":     tracking.PrereqMissing,
						"binary":     p,
						"agent_type": agentType,
						"configured": true,
						"message":    "the CLI path set in Settings > CLI paths can't be run: fix it or clear it to use the one on PATH",
					}))
					return
				}
				binaryPath = p
			}
		}
	}

	// Analytics tracks only the known tools checked by agent type; a caller-
	// supplied binary path is not a prerequisite observation.
	tool := prerequisiteTool(agentType)
	trackable := tool != "" && r.URL.Query().Get("binary") == ""
	source := tracking.SourceCLICheck
	if r.URL.Query().Get("source") == tracking.SourceCLIRecheck {
		source = tracking.SourceCLIRecheck
	}

	// Try LookPath first, then common install locations
	resolvedPath, err := cliLookPath(binaryPath)
	if err != nil {
		if found := cliCommonPath(binaryPath); found != "" {
			resolvedPath = found
		} else {
			result := map[string]any{
				"found":      false,
				"status":     tracking.PrereqMissing,
				"binary":     binaryPath,
				"agent_type": agentType,
			}
			if installCmd != "" {
				result["install_command"] = installCmd
			}
			if installURL != "" {
				result["install_url"] = installURL // the vendor's own page, shown as a link (#4569)
			}
			if installNote != "" {
				result["install_note"] = installNote
			}
			if trackable {
				tracking.TrackPrerequisite(tool, tracking.PrereqMissing, source)
			}
			writeJSON(w, http.StatusOK, hubCLIReply(r, result))
			return
		}
	}

	// Try to get version. A failed or timed-out probe is reported separately
	// from "found": the binary exists, but running it did not work.
	version, probe := probeCLIVersion(r.Context(), resolvedPath)
	status := tracking.PrereqAvailable
	switch probe {
	case probeFailed:
		status = tracking.PrereqProbeFailed
	case probeTimeout:
		status = tracking.PrereqTimeout
	}
	if trackable && probe != probeCanceled {
		tracking.TrackPrerequisite(tool, status, source)
	}

	writeJSON(w, http.StatusOK, hubCLIReply(r, map[string]any{
		"found":      true,
		"status":     status,
		"probe":      probe,
		"path":       resolvedPath,
		"version":    version,
		"agent_type": agentType,
	}))
}

// hubCLIReply shapes a prerequisite-check answer for the caller (#4579). Through Coral Hub nothing in it names this computer's
// account: the resolved path is left out (the Hub needs found, status and probe), a configured path that cannot be run is shown as the
// CLI's own name (the message already says "the CLI path set in Settings"), and the version is only the version number, not the
// CLI's whole first line, which a wrapper script can fill with anything. A local caller's answer is unchanged.
func hubCLIReply(r *http.Request, m map[string]any) map[string]any {
	if !cloud.Tunneled(r) {
		return m
	}
	delete(m, "path")
	if info := agent.GetCLIInfo(r.URL.Query().Get("type")); info != nil {
		if _, ok := m["binary"]; ok {
			m["binary"] = info.Binary
		}
	} else {
		delete(m, "binary")
	}
	if v, ok := m["version"].(string); ok {
		m["version"] = agent.VersionNumber(v)
	}
	return m
}

// Seams for the CLI and tmux prerequisite checks; tests replace them so no real
// executable or user directory is consulted.
var (
	cliLookPath     = exec.LookPath
	cliCommonPath   = agent.FindCLIInCommonPaths
	cliProbeTimeout = 3 * time.Second
	tmuxProbe       = tmux.IsAvailable
)

// Version probe results.
const (
	probeOK       = "ok"
	probeFailed   = "failed"
	probeTimeout  = "timeout"
	probeCanceled = "canceled" // the caller went away; not an observation
)

// prerequisiteTool maps an agent type to the tool name analytics may report.
func prerequisiteTool(agentType string) string {
	switch agentType {
	case "claude":
		return tracking.ToolClaude
	case "codex":
		return tracking.ToolCodex
	}
	return ""
}

// probeCLIVersion runs "<binary> --version" under a bounded timeout. It returns
// the first line of output and a classification; raw errors are never returned.
const maxCLIVersionBytes = 8 * 1024

type cliVersionOutput struct{ strings.Builder }

func (b *cliVersionOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxCLIVersionBytes - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Builder.Write(p)
	}
	// Drain excess output without allocating or breaking the child's pipe.
	return n, nil
}

func probeCLIVersion(parent context.Context, path string) (version, probe string) {
	ctx, cancel := context.WithTimeout(parent, cliProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = agentenv.Filter(os.Environ()) // the server's own CORAL_* settings never reach a CLI
	// A wrapper script can leave a child holding the output pipe after it is
	// killed; do not wait on it past this.
	cmd.WaitDelay = 200 * time.Millisecond
	var out cliVersionOutput
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		version = strings.TrimSpace(out.String())
		// Take first line only
		if idx := strings.IndexByte(version, '\n'); idx > 0 {
			version = version[:idx]
		}
		return version, probeOK
	}
	switch {
	case parent.Err() != nil:
		return "", probeCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", probeTimeout
	}
	return "", probeFailed
}
