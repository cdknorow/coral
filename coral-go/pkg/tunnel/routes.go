package tunnel

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
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
// segment, except {hex64}, which matches exactly 64 lowercase hex digits (a content digest). There are no prefix or wildcard rules, so a rule can never swallow a
// sibling route.
type route struct {
	method  string // "*" matches every method (used for explicit TierNever rules)
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
//	                                   (except GET /api/system/hub-operator, read only)
//	/api/tracking/*                    telemetry
//	/api/webhooks*                     outbound HTTP (SSRF) and stored URLs
//	/api/workflows, /api/scheduled     create/update/delete define commands that run later
//	/api/board/remotes*, /api/servers*, /api/remote/*   outbound requests / hub mode
//	/api/agent/*                       endpoints for agents on the machine; /api/agent/ui* serves agent-authored HTML
//	/api/cloud/*                       the connector's own settings
//	/api/indexer/*, /api/notifications, /api/call-metrics (POST)
var routeTable = cat(
	// ── explicit never rules ────────────────────────────────────────────
	// First match wins, so a static route that a later parametric rule would otherwise
	// swallow (for example /api/board/remotes vs /api/board/{project}) is pinned here.
	// internal/server's census fails when a registered static route is matched through a
	// {param} rule unless it is listed in testdata/cloud_route_exceptions.txt.
	rules("*", TierNever,
		"/api/board/remotes",
		"/api/board/remotes/{a}",
		"/api/board/remotes/{a}/{b}",
		"/api/board/remotes/{a}/{b}/{c}",
		"/api/board/remotes/{a}/{b}/{c}/{d}",
		"/api/board/remotes/{a}/{b}/{c}/{d}/{e}",
	),

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
		// Whether Coral Hub may act as the Operator on this computer (#3404): read only, so
		// the Hub can show it. Changing it (PUT) stays local (never).
		"/api/system/hub-operator",
		// A board's id, label and creation (#3536), so the Hub can label boards.
		"/api/board/{project}",
		"/api/board/{project}/messages",
		"/api/board/{project}/messages/all",
		"/api/board/{project}/messages/check",
		"/api/board/{project}/subscribers",
		"/api/board/{project}/peek",
		"/api/board/{project}/paused",
		"/api/board/{project}/settings",
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
		// A distillation run's status (#3518): fixed keys, bounded text, no paths.
		"/api/teams/detail/{name}/distill-knowledge",
		"/api/token-usage", "/api/token-usage/summary", "/api/token-usage/timeseries",
		"/api/token-usage/by-team", "/api/token-usage/by-branch", "/api/token-usage/session/{id}/turns",
		"/api/goals/metrics", "/api/call-metrics/summary",
		"/api/tags", "/api/folder-tags", "/api/folder-tags/{name}", "/api/pinned-folders",
		"/api/scheduled/jobs", "/api/scheduled/jobs/{id}", "/api/scheduled/jobs/{id}/runs", "/api/scheduled/runs/recent",
		"/api/workflows", "/api/workflows/runs/recent", "/api/workflows/by-name/{name}", "/api/workflows/{id}",
		"/api/workflows/{id}/runs", "/api/workflows/runs/{id}",
		"/api/themes", "/api/themes/variables", "/api/themes/{name}",
		"/api/templates/agents", "/api/templates/agents/{category}", "/api/templates/agents/{category}/{name}",
		"/api/templates/commands", "/api/templates/commands/{category}", "/api/templates/commands/{category}/{name}",
		"/api/views", "/api/views/{id}",
		"/api/agent-models", "/api/settings/default-prompts", "/api/session-artifacts",
		// A content-addressed agent artifact, named by its full SHA-256. Reads only; the exact 64-hex shape is
		// enforced by the {hex64} segment, and the only query is the link form v=2 (routes_query.go, #3179). Upload (POST /api/agent/artifacts) and
		// the artifact list under /api/agent/ stay unlisted (never).
		"/api/artifacts/{hex64}",
		// Agent UI (option B): the panel list, a panel's HTML (read as text and run in a sandbox by the dashboard) and its saved
		// events. {uiid} is exactly the machine's panel id shape. Publishing and removing panels (PUT/DELETE) are the agent's own
		// actions on its machine and stay unlisted (never).
		"/api/agent/ui",
		"/api/agent/ui/{uiid}/content",
		"/api/agent/ui/{uiid}/events",
	),

	// ── interact: typing into running agents, and changing running work ──
	rules(http.MethodPost, TierInteract,
		// A panel interaction and a request to the agent put user text in front of an agent, like send.
		"/api/agent/ui/{uiid}/events",
		"/api/agent/ui-request",
		"/api/sessions/live/{name}/send",
		"/api/sessions/live/{name}/keys",
		"/api/sessions/live/{name}/resize",
		"/api/sessions/live/{name}/answer-prompt",
		// Turning auto-accept off is always safe (#3182); turning it on is Launch, below.
		"/api/sessions/live/{name}/auto-accept/disable",
		"/api/auto-accept/disable-all",
		"/api/sessions/live/{name}/goal",
		"/api/sessions/live/{name}/files/refresh",
		"/api/sessions/live/{name}/kill",
		"/api/sessions/live/{name}/notes",
		"/api/sessions/live/{name}/tasks",
		"/api/sessions/live/{name}/tasks/reorder",
		"/api/sessions/live/team/{board}/sleep",
		"/api/sessions/live/sleep-all",
		"/api/sessions/live/{id}/sleep",
		// Archive stops a running agent like sleep; unarchive starts nothing (it stays asleep).
		"/api/sessions/live/{id}/archive",
		"/api/sessions/live/{id}/unarchive",
		"/api/tasks/runs/{id}/kill",
		"/api/workflows/runs/{id}/kill",
		"/api/board/{project}/messages",
		"/api/board/{project}/messages/mark-read",
		"/api/board/{project}/external-messages",
		// Coral Hub's notice about a team link (#3489): only with its stamped header.
		"/api/board/{project}/hub-notice",
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
		// Only the changed-files comparison base (validated to three values); PUT /api/settings itself stays never.
		"/api/settings/git-diff-mode",
		"/api/sessions/live/{name}/display-name",
		"/api/sessions/live/{name}/icon",
		"/api/sessions/live/{name}/name-color",
		"/api/board/{project}/working-mode",
		"/api/board/{project}/working-mode/presets/{id}",
		// PUT /api/board/{project}/settings (cross-team settings) is never through the tunnel (#3377).
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
	),
	// Deleting a whole board (its messages, tasks and members) can't be undone: Launch, not
	// Interact (#3433). It stays the Operator's (attributed, recorded, refused with the opt-out).
	rules(http.MethodDelete, TierLaunch, "/api/board/{project}"),
	// Killing every agent of a team at once ends all their work in progress: Launch (#3433).
	// Stopping one agent stays Interact (a safety stop).
	rules(http.MethodPost, TierLaunch, "/api/sessions/live/team/{board}/kill"),

	// ── launch: starts or wakes processes, browses folders, opens editors ─
	rules(http.MethodPost, TierLaunch,
		// Auto-accept lets the agent run tools without asking, as much as launching one
		// with permissions bypassed (#3182, Coral Hub review). The owner's local switch
		// /api/cloud/allow-auto-accept is never in this table.
		"/api/sessions/live/{name}/auto-accept/enable",
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
		// Distilling a team's knowledge runs the Claude CLI and spends model usage (#3518):
		// no query, no body, one run per team at a time, a minimum interval between runs.
		"/api/teams/detail/{name}/distill-knowledge",
		"/api/teams/import",
		"/api/teams/generate",
		"/api/tasks/run",
		"/api/workflows/{id}/trigger",
		"/api/workflows/by-name/{name}/trigger",
	),
	rules(http.MethodGet, TierLaunch, "/api/filesystem/list", "/api/filesystem/is-git"),
	// Pinning a folder to the Agents list names a folder to launch into (#3630): Launch, like
	// browsing for one. Reading the pins is View (above).
	rules(http.MethodPost, TierLaunch, "/api/pinned-folders"),
	rules(http.MethodDelete, TierLaunch, "/api/pinned-folders"),
	// File writes on the host are launch-tier (operator decision): they change what
	// runs next on that machine as surely as starting an agent does.
	rules(http.MethodPut, TierLaunch, "/api/sessions/live/{name}/file-content"),
	rules(http.MethodPost, TierLaunch, "/api/upload"),
)

// shapeOf is the table key used by routeQueries: parameter names do not matter.
func shapeOf(method, pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, sg := range segs {
		if strings.HasPrefix(sg, "{") {
			segs[i] = "{}"
		}
	}
	return method + " " + strings.Join(segs, "/")
}

type paramKind int

const (
	paramPlain paramKind = iota + 1
	paramFile
	paramEnum // only the exact values listed for it in the table, and only once
)

// paramSpec is how one query parameter is validated.
type paramSpec struct {
	kind   paramKind
	values []string // paramEnum: the accepted values
}

// querySpecs is routeQueries parsed: for each route shape, the parameters it accepts.
var querySpecs = func() map[string]map[string]paramSpec {
	out := make(map[string]map[string]paramSpec, len(routeQueries))
	for shape, spec := range routeQueries {
		m := map[string]paramSpec{}
		for _, p := range strings.Split(spec, ",") {
			if name, vals, ok := strings.Cut(p, "="); ok {
				m[name] = paramSpec{kind: paramEnum, values: strings.Split(vals, "|")}
			} else if strings.HasSuffix(p, "*") {
				m[strings.TrimSuffix(p, "*")] = paramSpec{kind: paramFile}
			} else {
				m[p] = paramSpec{kind: paramPlain}
			}
		}
		out[shape] = m
	}
	return out
}()

var schemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

// normalizeRef percent-decodes v until it stops changing (at most 5 rounds). Coral
// decodes file references again itself, so a once-decoded %2F or file: URL must not
// pass. A value that still contains '%' afterwards, or does not decode cleanly, is refused.
func normalizeRef(v string) (string, bool) {
	for i := 0; i < 5; i++ {
		d, err := url.PathUnescape(v)
		if err != nil {
			return "", false
		}
		if d == v {
			break
		}
		v = d
	}
	return v, !strings.Contains(v, "%")
}

// safeRef reports whether v is a plain file reference. allowAbs is for the launch tier,
// where choosing a folder to launch in legitimately takes an absolute path.
func safeRef(v string, allowAbs bool) bool {
	if v == "" {
		return true
	}
	n, ok := normalizeRef(v)
	if !ok || n == "" {
		return n == "" && ok
	}
	if strings.ContainsAny(n, "#?\\:\x00") || schemeRe.MatchString(n) || strings.ContainsAny(n, compatDotSlash) {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	if n[0] == '~' || n[0] == '-' {
		return false
	}
	if n[0] == '/' && !allowAbs {
		return false
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// UnsafeFileRef reports whether v is not a plain project-relative reference: absolute,
// home-relative, option-like, a URL or drive path, percent-encoded past the point of
// decoding, containing backslashes, NUL, control or look-alike dot characters, or a
// ".." segment.
func UnsafeFileRef(v string) bool { return !safeRef(v, false) }

// queryOK enforces the per-route query table: only the parameters the handler reads are
// accepted, file references are normalised and checked, and everything else is refused.
func queryOK(r route, rawQuery string) bool {
	if rawQuery == "" {
		return true
	}
	if !validQuery(rawQuery) {
		return false
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	spec := querySpecs[shapeOf(r.method, r.pattern)]
	for name, vals := range q {
		ps, ok := spec[name]
		if !ok {
			return false // unknown parameter
		}
		if ps.kind == paramEnum && (len(vals) != 1 || !slices.Contains(ps.values, vals[0])) {
			return false // exact value, once: no look-alikes, no repeats
		}
		for _, v := range vals {
			if ps.kind == paramFile && !safeRef(v, r.tier == TierLaunch) {
				return false
			}
		}
	}
	return true
}

func methodMatches(rule, method string) bool { return rule == "*" || rule == method }

// RuleFor returns the table pattern and tier that classify a request, ignoring its query.
// It exists for the route census in internal/server.
func RuleFor(method, decodedPath string) (pattern string, tier Tier, ok bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs, valid := splitSegments(decodedPath)
	if !valid {
		return "", TierNever, false
	}
	for _, r := range routeTable {
		if methodMatches(r.method, method) && matchSegments(r.pattern, segs) {
			return r.pattern, r.tier, true
		}
	}
	return "", TierNever, false
}

// Classify returns the tier of a request whose path is already decoded and has passed
// ValidatePath, or TierNever when the route is not in the table or its query is refused.
// The first matching rule decides.
func Classify(method, decodedPath, rawQuery string) Tier {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs, ok := splitSegments(decodedPath)
	if !ok {
		return TierNever
	}
	for _, r := range routeTable {
		if methodMatches(r.method, method) && matchSegments(r.pattern, segs) {
			if r.tier == TierNever || !queryOK(r, rawQuery) {
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
		if ps == "{hex64}" {
			if !isHex64(segs[i]) {
				return false
			}
			continue
		}
		if ps == "{uiid}" {
			if !isUIID(segs[i]) {
				return false
			}
			continue
		}
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

// isHex64 reports whether s is exactly 64 lowercase hexadecimal digits.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// isUIID reports whether s has the shape of an Agent UI panel id: 1 to 64 of [A-Za-z0-9_-].
func isUIID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
