package cloud

import (
	"net/http"
	"strings"
)

// Class says what a route may do when the hub asks for it.
type Class int

const (
	// ClassDeny is the default: the connector never forwards it.
	ClassDeny Class = iota
	// ClassRead is read-only data needed by the dashboard.
	ClassRead
	// ClassWrite changes agent, task, board or terminal state (chat, keys, tasks,
	// board posts, stop). It does not start new processes.
	ClassWrite
	// ClassLaunch starts agent or workflow processes, or browses the local file
	// system to choose a folder for one. It is forwarded only while the local
	// "allow launching from the web" switch is on (spec: separate switch).
	ClassLaunch
)

func (c Class) String() string {
	switch c {
	case ClassRead:
		return "read"
	case ClassWrite:
		return "write"
	case ClassLaunch:
		return "launch"
	}
	return "deny"
}

// TunnelHeader is added by the connector to every request it forwards. Handlers of
// routes that read or change local state in ways a remote web user must not (absolute
// file paths, the connector's own settings, API keys, editors) refuse requests that
// carry it. The hub cannot forge or remove it: hub headers are scrubbed to an
// allowlist and this one is set afterwards.
const TunnelHeader = "X-Coral-Tunnel"

// rule is "METHOD /path/{param}/more" where a {param} matches exactly one non-empty
// segment, and a trailing "/*" matches one or more further segments.
type rule struct {
	method  string
	pattern string
	class   Class
}

func get(c Class, patterns ...string) []rule {
	var out []rule
	for _, p := range patterns {
		out = append(out, rule{http.MethodGet, p, c})
	}
	return out
}

func with(method string, c Class, patterns ...string) []rule {
	var out []rule
	for _, p := range patterns {
		out = append(out, rule{method, p, c})
	}
	return out
}

func join(groups ...[]rule) []rule {
	var out []rule
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// The route table is an ALLOWLIST. Everything not listed is denied, including every
// route added to the server later; internal/server's golden test forces a conscious
// decision for each new route.
//
// Deliberately NOT listed (denied), with the reason:
//
//	/api/settings (GET, PUT)             PUT can set cli_path_* (code execution) and remote access
//	/api/system/*                        API key read/rotate, DB security, editors, QR/network info, privacy
//	/api/license/*, /api/tracking/*      license and telemetry
//	/api/upload, PUT .../file-content    writes files on the host
//	POST .../open-in-editor              starts an editor on the host
//	/api/webhooks*                       outbound HTTP (SSRF) and stored URLs
//	/api/workflows (create/update/delete), /api/scheduled/jobs (create/update/delete/toggle)
//	                                     define commands that run later
//	/api/board/remotes*                  configures outbound polling of other servers
//	/api/servers*, /api/remote/*         hub mode only; never reachable through a tunnel
//	/api/agent/*                         endpoints for agents on this machine (hooks), not the UI;
//	                                     /api/agent/ui* serves agent-authored HTML
//	/api/cloud/*                         the connector's own settings
//	/api/indexer/refresh, /api/notifications, /api/call-metrics (POST)  not needed remotely
var table = join(
	// ── liveness and WebSocket feeds ─────────────────────────────────────
	get(ClassRead, "/api/health", "/ws/coral", "/ws/terminal/{name}"),

	// ── live sessions: reads ─────────────────────────────────────────────
	get(ClassRead,
		"/api/sessions/live",
		"/api/sessions/live/{name}",
		"/api/sessions/live/{name}/capture",
		"/api/sessions/live/{name}/poll",
		"/api/sessions/live/{name}/chat",
		"/api/sessions/live/{name}/info",
		"/api/sessions/live/{name}/files",
		"/api/sessions/live/{name}/diff",
		"/api/sessions/live/{name}/changes.diff",
		"/api/sessions/live/{name}/search-files",
		"/api/sessions/live/{name}/git",
		"/api/sessions/live/{name}/file-content", // absolute paths refused for tunneled requests (TunnelHeader)
		"/api/sessions/live/{name}/file-original",
		"/api/sessions/live/{name}/resolve-path",
		"/api/sessions/live/{name}/tasks",
		"/api/sessions/live/{name}/history/search",
		"/api/sessions/live/{name}/history/context",
		"/api/sessions/live/{name}/subagents",
		"/api/sessions/live/{name}/subagents/{id}",
		"/api/sessions/live/{name}/notes",
		"/api/sessions/live/{name}/events",
		"/api/sessions/live/{name}/events/counts",
		"/api/sessions/live/{name}/pending-tool",
		"/api/sessions/live/{name}/prompt-options",
		"/api/sessions/live/team/{board}/sleep-status",
		"/api/sessions/resolve",
		"/api/sessions/{id}/resolve",
		"/api/sessions/{id}/status",
		"/api/sessions/{id}/changes",
		"/api/sessions/{id}/changes.diff",
	),

	// ── live sessions: writes that do not start processes ────────────────
	with(http.MethodPost, ClassWrite,
		"/api/sessions/live/{name}/send",
		"/api/sessions/live/{name}/keys",
		"/api/sessions/live/{name}/resize",
		"/api/sessions/live/{name}/answer-prompt",
		"/api/sessions/live/{name}/goal",
		"/api/sessions/live/{name}/files/refresh",
		"/api/sessions/live/{name}/kill",
		"/api/sessions/live/{name}/notes",
		"/api/sessions/live/{name}/tasks",
		"/api/sessions/live/{name}/tasks/reorder",
		"/api/sessions/live/team/{board}/kill",
		"/api/sessions/live/team/{board}/reset",
		"/api/sessions/live/team/{board}/sleep",
		"/api/sessions/live/sleep-all",
		"/api/sessions/live/{id}/sleep",
		"/api/tasks/runs/{id}/kill",
	),
	with(http.MethodPut, ClassWrite,
		"/api/sessions/live/{name}/display-name",
		"/api/sessions/live/{name}/icon",
		"/api/sessions/live/{name}/name-color",
	),
	with(http.MethodPatch, ClassWrite,
		"/api/sessions/live/{name}/notes/{id}",
		"/api/sessions/live/{name}/tasks/{id}",
	),
	with(http.MethodDelete, ClassWrite,
		"/api/sessions/live/{name}/notes/{id}",
		"/api/sessions/live/{name}/tasks/{id}",
	),

	// ── launch: starts processes, or browses folders to choose one ───────
	with(http.MethodPost, ClassLaunch,
		"/api/sessions/launch",
		"/api/sessions/launch-team",
		"/api/sessions/live/{name}/resume",
		"/api/sessions/live/{name}/restart",
		"/api/sessions/live/{name}/attach",
		"/api/sessions/live/{id}/wake",
		"/api/sessions/live/team/{board}/wake",
		"/api/sessions/live/wake-all",
		"/api/teams/detail/{name}/resurrect",
		"/api/teams/import",
		"/api/teams/generate",
		"/api/tasks/run",
		"/api/workflows/{id}/trigger",
		"/api/workflows/by-name/{name}/trigger",
	),
	get(ClassLaunch, "/api/filesystem/list", "/api/filesystem/is-git"),

	// ── history ──────────────────────────────────────────────────────────
	get(ClassRead,
		"/api/sessions/history",
		"/api/sessions/history/search",
		"/api/sessions/history/{id}",
		"/api/sessions/history/{id}/agent-notes",
		"/api/sessions/history/{id}/notes",
		"/api/sessions/history/{id}/tags",
		"/api/sessions/history/{id}/git",
		"/api/sessions/history/{id}/events",
		"/api/sessions/history/{id}/tasks",
		"/api/sessions/history/{id}/files",
		"/api/sessions/history/{id}/resume-info",
	),

	// ── board and tasks ──────────────────────────────────────────────────
	get(ClassRead,
		"/api/board/projects",
		"/api/board/tasks",
		"/api/board/{project}/messages",
		"/api/board/{project}/messages/all",
		"/api/board/{project}/messages/check",
		"/api/board/{project}/subscribers",
		"/api/board/{project}/peek",
		"/api/board/{project}/paused",
		"/api/board/{project}/groups",
		"/api/board/{project}/groups/{id}/members",
		"/api/board/{project}/waits",
		"/api/board/{project}/waits/poll",
		"/api/board/{project}/tasks",
		"/api/board/{project}/tasks/{id}",
		"/api/board/{project}/tasks/{id}/cost",
		"/api/board/{project}/tasks/{id}/changes.diff",
		"/api/board/{project}/tasks/{id}/artifact-content",
		"/api/board/{project}/artifacts",
		"/api/board/{project}/status",
		"/api/board/{project}/working-mode",
		"/api/board/{project}/working-mode/presets",
		"/api/tasks/runs", "/api/tasks/active", "/api/tasks/runs/{id}",
	),
	with(http.MethodPost, ClassWrite,
		"/api/board/{project}/messages",
		"/api/board/{project}/messages/mark-read",
		"/api/board/{project}/subscribe",
		"/api/board/{project}/pause",
		"/api/board/{project}/resume",
		"/api/board/{project}/reminder",
		"/api/board/{project}/groups/{id}/members",
		"/api/board/{project}/waits",
		"/api/board/{project}/tasks",
		"/api/board/{project}/tasks/claim",
		"/api/board/{project}/tasks/current",
		"/api/board/{project}/tasks/{id}/complete",
		"/api/board/{project}/tasks/{id}/submit-review",
		"/api/board/{project}/tasks/{id}/release-review",
		"/api/board/{project}/tasks/{id}/cancel",
		"/api/board/{project}/tasks/{id}/reassign",
		"/api/board/{project}/tasks/{id}/nudge",
		"/api/board/{project}/tasks/{id}/snooze",
		"/api/board/{project}/tasks/{id}/reminder",
		"/api/board/{project}/tasks/{id}/publish",
		"/api/board/{project}/working-mode/presets",
		"/api/board/{project}/working-mode/presets/{id}/reset",
	),
	with(http.MethodPut, ClassWrite,
		"/api/board/{project}/working-mode",
		"/api/board/{project}/working-mode/presets/{id}",
	),
	with(http.MethodPatch, ClassWrite,
		"/api/board/{project}/tasks/{id}",
		"/api/board/{project}/tasks/{id}/amend",
	),
	with(http.MethodDelete, ClassWrite,
		"/api/board/{project}/messages/{id}",
		"/api/board/{project}/subscribe",
		"/api/board/{project}/reminder",
		"/api/board/{project}/waits",
		"/api/board/{project}/groups/{group}/members/{id}",
		"/api/board/{project}/tasks/{id}/reminder",
		"/api/board/{project}",
	),

	// ── teams, usage, tags, views, workflows (read) ──────────────────────
	get(ClassRead,
		"/api/teams/all",
		"/api/teams/detail/{name}",
		"/api/teams/detail/{name}/availability",
		"/api/teams/detail/{name}/knowledge",
		"/api/teams/detail/{name}/knowledge/{agent}",
		"/api/token-usage", "/api/token-usage/summary", "/api/token-usage/timeseries",
		"/api/token-usage/by-team", "/api/token-usage/by-branch", "/api/token-usage/session/{id}/turns",
		"/api/goals/metrics", "/api/call-metrics/summary",
		"/api/tags", "/api/folder-tags", "/api/folder-tags/{name}",
		"/api/scheduled/jobs", "/api/scheduled/jobs/{id}", "/api/scheduled/jobs/{id}/runs", "/api/scheduled/runs/recent",
		"/api/workflows", "/api/workflows/runs/recent", "/api/workflows/by-name/{name}", "/api/workflows/{id}",
		"/api/workflows/{id}/runs", "/api/workflows/runs/{id}",
		"/api/themes", "/api/themes/variables", "/api/themes/{name}",
		"/api/templates/agents", "/api/templates/agents/{category}", "/api/templates/agents/{category}/{name}",
		"/api/templates/commands", "/api/templates/commands/{category}", "/api/templates/commands/{category}/{name}",
		"/api/views", "/api/views/{id}",
		"/api/agent-models", "/api/settings/default-prompts", "/api/session-artifacts",
	),
	with(http.MethodPost, ClassWrite, "/api/workflows/runs/{id}/kill"),
)

// Classify returns the class of a request whose path is already decoded and has
// passed tunnel.ValidatePath. Anything not in the table is ClassDeny.
func Classify(method, decodedPath string) Class {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs, ok := splitPath(decodedPath)
	if !ok {
		return ClassDeny
	}
	for _, r := range table {
		if r.method == method && matchSegs(r.pattern, segs) {
			return r.class
		}
	}
	return ClassDeny
}

func splitPath(p string) ([]string, bool) {
	if !strings.HasPrefix(p, "/") || p == "/" {
		return nil, false
	}
	segs := strings.Split(p[1:], "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
			return nil, false
		}
	}
	return segs, true
}

func matchSegs(pattern string, segs []string) bool {
	pat := strings.Split(pattern[1:], "/")
	if len(pat) != len(segs) {
		return false
	}
	for i, ps := range pat {
		if strings.HasPrefix(ps, "{") && strings.HasSuffix(ps, "}") {
			continue
		}
		if ps != segs[i] {
			return false
		}
	}
	return true
}

// Rules returns the AllowPath function for tunnel.LocalHandler. allowLaunch is
// consulted on every launch-class request, so flipping the local switch takes effect
// immediately and a compromised hub cannot change it. A nil allowLaunch means off.
func Rules(allowLaunch func() bool) func(method, decodedPath string) bool {
	return func(method, decodedPath string) bool {
		switch Classify(method, decodedPath) {
		case ClassRead, ClassWrite:
			return true
		case ClassLaunch:
			return allowLaunch != nil && allowLaunch()
		}
		return false
	}
}

// Routes lists the table for the golden test: "METHOD pattern class".
func Routes() []string {
	out := make([]string, 0, len(table))
	for _, r := range table {
		out = append(out, r.method+" "+r.pattern+" "+r.class.String())
	}
	return out
}
