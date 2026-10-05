package tracking

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// SchemaVersion identifies the event/property contract. Bump it when an event
// or property changes meaning so downstream analysis can tell the versions apart.
const SchemaVersion = 2

// runID is generated once per server process. It is random, never persisted
// and unrelated to the install ID, so it can group one run's events without
// identifying anything.
var runID = uuid.NewString()

// RunID returns this process's run identifier.
func RunID() string { return runID }

// Additional event names. The lifecycle, usage, conversion and milestone names
// live in events.go and milestones.go.
const (
	EventLaunchRequested       = "launch_requested"
	EventLaunchResult          = "launch_result"
	EventTaskCompleted         = "task_completed"
	EventDashboardReady        = "dashboard_ready"
	EventDashboardActiveDay    = "dashboard_active_day"
	EventDashboardFailed       = "dashboard_failed"
	EventFirstPromptSubmitted  = "first_prompt_submitted"
	EventFirstTaskSucceeded    = "first_task_succeeded"
	EventPromptSubmitRequested = "prompt_submit_requested"
)

// Launch vocabulary. Every value an event can carry is drawn from a closed
// set like these; free text never reaches an event.
const (
	KindAgent = "agent"
	KindTeam  = "team"

	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomePartial = "partial" // team: some agents started, some did not
	OutcomeFailed  = "failed"  // task outcome

	FailInvalidRequest      = "invalid_request"
	FailUnsupportedOption   = "unsupported_option"
	FailLimitReached        = "limit_reached"
	FailWorkdirUnavailable  = "workdir_unavailable"
	FailBackendUnavailable  = "backend_unavailable"
	FailProviderUnavailable = "provider_unavailable"
	FailSpawnFailed         = "spawn_failed"
	FailWorktreeFailed      = "worktree_failed"
	FailInternal            = "internal"
)

type propKind int

const (
	kindEnum propKind = iota
	kindInt
	kindBool
	kindSlug // short [A-Za-z0-9_.-] token
	kindLabel
)

type propSpec struct {
	kind     propKind
	enum     []string
	min, max int
}

func enum(values ...string) propSpec { return propSpec{kind: kindEnum, enum: values} }
func intRange(lo, hi int) propSpec   { return propSpec{kind: kindInt, min: lo, max: hi} }

var (
	slugProp    = propSpec{kind: kindSlug}
	labelProp   = propSpec{kind: kindLabel}
	boolProp    = propSpec{kind: kindBool}
	providerEnu = enum("claude", "codex", "gemini", "other", "mixed")
	backendEnu  = enum("tmux", "pty", "unknown")
	kindEnu     = enum(KindAgent, KindTeam)
	failureEnu  = enum(FailInvalidRequest, FailUnsupportedOption, FailLimitReached, FailWorkdirUnavailable,
		FailBackendUnavailable, FailProviderUnavailable, FailSpawnFailed, FailWorktreeFailed, FailInternal)
	agentCount = intRange(0, 64)
)

var (
	slugRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	labelRE = regexp.MustCompile(`^[A-Za-z0-9 _.+()-]{1,64}$`)
)

// eventSpecs is the closed allowlist of events and, per event, the only
// properties it may carry beyond the standard ones. An event or property not
// listed here cannot be sent.
var eventSpecs = map[string]map[string]propSpec{
	EventInstall:   {},
	EventUpgrade:   {},
	EventAppOpened: {},

	EventSessionLaunched: {},
	EventTeamLaunched:    {"agent_count": agentCount, "requested_agents": agentCount, "started_agents": agentCount, "failed_agents": agentCount},

	EventFirstAgentLaunched: {},
	EventFirstTeamLaunched:  {"agent_count": agentCount},
	EventFirstTaskCompleted: {"outcome": enum(OutcomeSuccess, OutcomeFailed)},
	EventFirstTaskSucceeded: {},
	EventReturned24h:        {},

	EventSupporterCheckoutClicked: {"surface": slugProp, "campaign": slugProp, "source": slugProp, "medium": slugProp},
	EventLicenseActivated:         {"product_name": labelProp, "variant_name": labelProp},

	EventLaunchRequested: {"kind": kindEnu, "attempt_id": slugProp, "provider": providerEnu, "backend": backendEnu, "requested_agents": agentCount, "resume": boolProp},
	EventLaunchResult: {"kind": kindEnu, "attempt_id": slugProp, "provider": providerEnu, "backend": backendEnu, "requested_agents": agentCount,
		"resume": boolProp, "outcome": enum(OutcomeSuccess, OutcomeFailure, OutcomePartial), "failure_category": failureEnu,
		"duration_ms": intRange(0, 600000), "started_agents": agentCount, "failed_agents": agentCount},
	EventTaskCompleted: {"outcome": enum(OutcomeSuccess, OutcomeFailed)},

	EventDashboardReady:        {"page_id": slugProp},
	EventDashboardActiveDay:    {},
	EventDashboardFailed:       {"code": enum("sessions_fetch_http", "sessions_fetch_network", "sessions_fetch_invalid", "status_fetch_http", "status_fetch_network", "status_fetch_invalid", "init_failed")},
	EventFirstPromptSubmitted:  {"source": enum("http_send")},
	EventPromptSubmitRequested: {"source": enum("dashboard_composer")},
}

// KnownEvent reports whether name is on the allowlist.
func KnownEvent(name string) bool {
	_, ok := eventSpecs[name]
	return ok
}

// sanitizeProps validates extra against the event's property allowlist and
// returns typed values (numbers and booleans stay typed). Unknown keys and
// values outside their bounds are dropped; the second result is false only for
// an event that is not on the allowlist.
func sanitizeProps(event string, extra map[string]string) (map[string]any, bool) {
	spec, ok := eventSpecs[event]
	if !ok {
		return nil, false
	}
	out := map[string]any{}
	for key, raw := range extra {
		p, allowed := spec[key]
		if !allowed {
			continue
		}
		switch p.kind {
		case kindEnum:
			for _, v := range p.enum {
				if raw == v {
					out[key] = raw
					break
				}
			}
		case kindInt:
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= p.min && n <= p.max {
				out[key] = n
			}
		case kindBool:
			if raw == "true" || raw == "false" {
				out[key] = raw == "true"
			}
		case kindSlug:
			if slugRE.MatchString(raw) {
				out[key] = raw
			}
		case kindLabel:
			if labelRE.MatchString(raw) {
				out[key] = raw
			}
		}
	}
	return out, true
}

// ── Launch attempts ──────────────────────────────────────────────────────

// Attempt correlates a launch's requested and result events. attempt_id is a
// random per-attempt token, not derived from any session, team or board.
type Attempt struct {
	id        string
	kind      string
	provider  string
	backend   string
	requested int
	resume    bool
	start     time.Time
}

// StartLaunch records launch_requested and returns the attempt to finish. It is
// safe to call and to ignore the result of when telemetry is off.
func StartLaunch(kind, provider, backend string, requestedAgents int, resume bool) *Attempt {
	a := &Attempt{id: uuid.NewString(), kind: kind, provider: provider, backend: backend, requested: requestedAgents, resume: resume, start: time.Now()}
	TrackEvent(EventLaunchRequested, a.props())
	return a
}

func (a *Attempt) props() map[string]string {
	return map[string]string{
		"kind": a.kind, "attempt_id": a.id, "provider": a.provider, "backend": a.backend,
		"requested_agents": strconv.Itoa(a.requested), "resume": strconv.FormatBool(a.resume),
	}
}

// Finish records launch_result. category is one of the Fail* constants and is
// sent for failure and partial outcomes. started/failed count agents (team).
func (a *Attempt) Finish(outcome, category string, started, failed int) {
	if a == nil {
		return
	}
	p := a.props()
	p["outcome"] = outcome
	if outcome != OutcomeSuccess && category != "" {
		p["failure_category"] = category
	}
	p["duration_ms"] = strconv.FormatInt(time.Since(a.start).Milliseconds(), 10)
	p["started_agents"] = strconv.Itoa(started)
	p["failed_agents"] = strconv.Itoa(failed)
	TrackEvent(EventLaunchResult, p)
}

// ProviderFor maps an agent type to the closed provider enum. A blank type
// means Claude, the fallback launchSession actually applies.
func ProviderFor(agentType string) string {
	t := strings.ToLower(strings.TrimSpace(agentType))
	switch t {
	case "":
		return "claude"
	case "claude", "codex", "gemini":
		return t
	}
	return "other"
}

// TeamProvider labels a team by the EFFECTIVE provider of its launched members:
// the single provider they share, or "mixed" only when they truly differ.
func TeamProvider(memberTypes []string) string {
	seen := ""
	for _, t := range memberTypes {
		p := ProviderFor(t)
		if seen == "" {
			seen = p
		} else if p != seen {
			return "mixed"
		}
	}
	if seen == "" {
		return "claude"
	}
	return seen
}

// ── Entrypoint ───────────────────────────────────────────────────────────

var entrypoint atomic.Value // string

// SetEntrypoint records which Coral executable this process is, as a closed
// enum, so events from the CLI server and the tray app can be told apart.
// Anything else is stored as "unknown". Call it before any tracking.
func SetEntrypoint(name string) {
	switch name {
	case "coral", "coral-tray", "launch-coral":
		entrypoint.Store(name)
	default:
		entrypoint.Store("unknown")
	}
}

func entrypointName() string {
	if v, ok := entrypoint.Load().(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// ── Task outcomes ────────────────────────────────────────────────────────

// TrackTaskCompleted records a finished board task. outcome is "success" or
// "failed". first_task_completed keeps its original meaning (any completion);
// first_task_succeeded is the distinct milestone for a successful one.
func TrackTaskCompleted(outcome string) {
	if outcome != OutcomeFailed {
		outcome = OutcomeSuccess
	}
	TrackEvent(EventTaskCompleted, map[string]string{"outcome": outcome})
	TrackOnce(EventFirstTaskCompleted, map[string]string{"outcome": outcome})
	if outcome == OutcomeSuccess {
		TrackOnce(EventFirstTaskSucceeded, nil)
	}
}
