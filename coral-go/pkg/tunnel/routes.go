package tunnel

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Tier is an access level. A request is allowed when its route's tier is at or below
// the effective access mode. The hub (defence in depth) and the connector (which
// cannot trust the hub) classify requests with the same table and the same code.
type Tier int

const (
	// TierNever is refused whatever the mode. It is also the answer for any route
	// not in the table, so a route added to Coral later is not exposed until someone
	// classifies it.
	TierNever Tier = iota
	// TierView reads: terminal output, chat, files, tasks, board, lists.
	TierView
	// TierInteract changes state of running work: types into agents (send, keys,
	// resize, answer-prompt) and edits tasks, notes, board, names, and stops agents.
	// Interact is already code execution through an agent with shell tools.
	TierInteract
	// TierLaunch starts or schedules processes, wakes agents, browses folders to choose
	// a launch directory, opens editors on the host and writes files there.
	TierLaunch
)

// Mode is the name of the highest tier a server grants; the same values as Tier.
type Mode = Tier

func (t Tier) String() string {
	switch t {
	case TierView:
		return "view"
	case TierInteract:
		return "interact"
	case TierLaunch:
		return "launch"
	}
	return "never"
}

// ParseMode parses "view", "interact" or "launch". Anything else is not a mode.
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "view":
		return TierView, true
	case "interact":
		return TierInteract, true
	case "launch":
		return TierLaunch, true
	}
	return TierNever, false
}

// Lower returns the smaller of two modes: the effective mode is the lower of the hub's
// per-server setting and the connector's local maximum.
func Lower(a, b Mode) Mode {
	if a < b {
		return a
	}
	return b
}

// route is "METHOD /path/{param}/more": a {param} matches exactly one non-empty
// segment. There are no prefix or wildcard rules, so a rule can never swallow a
// sibling route.
type route struct {
	method  string
	pattern string
	tier    Tier
}

func rules(method string, t Tier, patterns ...string) []route {
	out := make([]route, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, route{method, p, t})
	}
	return out
}

func cat(groups ...[]route) []route {
	var out []route
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// routeTable is an ALLOWLIST. Not listed means TierNever, including:
//
//	/api/settings (GET, PUT)           PUT can set cli_path_* (code execution) and remote access
//	/api/system/*                      API key read/rotate, DB security, editors, QR/network info, privacy
//	/api/license/*, /api/tracking/*    licence and telemetry
//	/api/webhooks*                     outbound HTTP (SSRF) and stored URLs
//	/api/workflows, /api/scheduled     create/update/delete define commands that run later
//	/api/board/remotes*, /api/servers*, /api/remote/*   outbound requests / hub mode
//	/api/agent/*                       endpoints for agents on the machine; /api/agent/ui* serves agent-authored HTML
//	/api/cloud/*                       the connector's own settings
//	/api/indexer/*, /api/notifications, /api/call-metrics (POST)
var routeTable = cat(
	// ── view: reads ─────────────────────────────────────────────────────
	rules(http.MethodGet, TierView,
		"/api/health", "/ws/coral", "/ws/terminal/{name}",
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
		"/api/sessions/live/{name}/file-content",
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

	// ── interact: typing into running agents, and changing running work ──
	rules(http.MethodPost, TierInteract,
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
		"/api/sessions/live/team/{board}/sleep",
		"/api/sessions/live/sleep-all",
		"/api/sessions/live/{id}/sleep",
		"/api/tasks/runs/{id}/kill",
		"/api/workflows/runs/{id}/kill",
		"/api/board/{project}/messages",
		"/api/board/{project}/messages/mark-read",
		"/api/board/{project}/subscribe",
		"/api/board/{project}/pause",
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
	rules(http.MethodPut, TierInteract,
		"/api/sessions/live/{name}/display-name",
		"/api/sessions/live/{name}/icon",
		"/api/sessions/live/{name}/name-color",
		"/api/board/{project}/working-mode",
		"/api/board/{project}/working-mode/presets/{id}",
	),
	rules(http.MethodPatch, TierInteract,
		"/api/sessions/live/{name}/notes/{id}",
		"/api/sessions/live/{name}/tasks/{id}",
		"/api/board/{project}/tasks/{id}",
		"/api/board/{project}/tasks/{id}/amend",
	),
	rules(http.MethodDelete, TierInteract,
		"/api/sessions/live/{name}/notes/{id}",
		"/api/sessions/live/{name}/tasks/{id}",
		"/api/board/{project}/messages/{id}",
		"/api/board/{project}/subscribe",
		"/api/board/{project}/reminder",
		"/api/board/{project}/waits",
		"/api/board/{project}/groups/{group}/members/{id}",
		"/api/board/{project}/tasks/{id}/reminder",
		"/api/board/{project}",
	),

	// ── launch: starts or wakes processes, browses folders, opens editors ─
	rules(http.MethodPost, TierLaunch,
		"/api/sessions/launch",
		"/api/sessions/launch-team",
		"/api/sessions/live/{name}/resume",
		"/api/sessions/live/{name}/restart",
		"/api/sessions/live/{name}/attach",
		"/api/sessions/live/{name}/open-in-editor",
		"/api/sessions/live/{id}/wake",
		"/api/sessions/live/team/{board}/wake",
		"/api/sessions/live/team/{board}/reset",
		"/api/sessions/live/wake-all",
		"/api/board/{project}/resume",
		"/api/teams/detail/{name}/resurrect",
		"/api/teams/import",
		"/api/teams/generate",
		"/api/tasks/run",
		"/api/workflows/{id}/trigger",
		"/api/workflows/by-name/{name}/trigger",
	),
	rules(http.MethodGet, TierLaunch, "/api/filesystem/list", "/api/filesystem/is-git"),
	// File writes on the host are launch-tier (operator decision): they change what
	// runs next on that machine as surely as starting an agent does.
	rules(http.MethodPut, TierLaunch, "/api/sessions/live/{name}/file-content"),
	rules(http.MethodPost, TierLaunch, "/api/upload"),
)

// fileParams name a file or directory in a query. Outside the launch tier they must be
// relative to the project: the local API reads absolute paths "wherever they are" for a
// user at the keyboard, which a remote web user must not inherit (P-8).
var fileParams = []string{"filepath", "dir", "path", "file", "ref", "cwd", "folder", "directory", "root", "working_dir"}

// UnsafeFileRef reports whether v is not a plain project-relative reference: absolute,
// home-relative, drive-lettered, option-like, containing backslashes, NUL or a ".." segment.
func UnsafeFileRef(v string) bool {
	if v == "" {
		return false
	}
	if v[0] == '/' || v[0] == '~' || v[0] == '-' || strings.ContainsAny(v, "\\\x00") || (len(v) > 1 && v[1] == ':') {
		return true
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func queryOK(tier Tier, rawQuery string) bool {
	if rawQuery == "" {
		return true
	}
	if !validQuery(rawQuery) {
		return false
	}
	if tier == TierLaunch {
		return true // folder browsing for a launch takes absolute paths; it needs launch mode
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	for _, p := range fileParams {
		for _, v := range q[p] {
			if UnsafeFileRef(v) {
				return false
			}
		}
	}
	return true
}

// Classify returns the tier of a request whose path is already decoded and has passed
// ValidatePath, or TierNever when the route is not in the table or its query is unsafe.
func Classify(method, decodedPath, rawQuery string) Tier {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs, ok := splitSegments(decodedPath)
	if !ok {
		return TierNever
	}
	for _, r := range routeTable {
		if r.method == method && matchSegments(r.pattern, segs) {
			if !queryOK(r.tier, rawQuery) {
				return TierNever
			}
			return r.tier
		}
	}
	return TierNever
}

// AllowRequest returns the query-aware check for LocalHandler: a request is allowed when
// its tier is known and no higher than the effective mode, read at request time so a
// mode change applies immediately. A nil mode function means view.
func AllowRequest(mode func() Mode) func(method, decodedPath, rawQuery string) bool {
	return func(method, decodedPath, rawQuery string) bool {
		t := Classify(method, decodedPath, rawQuery)
		return t != TierNever && t <= currentMode(mode)
	}
}

func currentMode(mode func() Mode) Mode {
	if mode == nil {
		return TierView
	}
	m := mode()
	if m < TierView || m > TierLaunch {
		return TierView
	}
	return m
}

// RoutePatterns lists "METHOD pattern tier" for tests and docs.
func RoutePatterns() []string {
	out := make([]string, 0, len(routeTable))
	for _, r := range routeTable {
		out = append(out, r.method+" "+r.pattern+" "+r.tier.String())
	}
	return out
}

func splitSegments(p string) ([]string, bool) {
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

func matchSegments(pattern string, segs []string) bool {
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

// FrameTier is the tier a client-to-server WebSocket message needs. Opening
// /ws/terminal/{name} is view (read the output); typing or resizing through it is
// interact. A frame that is not valid JSON, or whose type is unknown, needs interact:
// when in doubt it is input.
func FrameTier(decodedPath string, data []byte) Tier {
	if !strings.HasPrefix(decodedPath, "/ws/terminal/") {
		return TierView
	}
	var m struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return TierInteract
	}
	switch m.Type {
	case "ping", "pong", "heartbeat":
		return TierView
	}
	return TierInteract
}
