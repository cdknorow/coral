package tracking

import "sync"

// EventPrerequisiteCheck reports whether a tool Coral depends on is usable. It
// observes only; Coral does not run installers, so "missing" is never an
// installer failure.
const EventPrerequisiteCheck = "prerequisite_check"

// Prerequisite vocabulary (closed sets).
const (
	ToolTmux   = "tmux"
	ToolClaude = "claude"
	ToolCodex  = "codex"

	PrereqAvailable   = "available"    // found, and its version probe (if any) succeeded
	PrereqMissing     = "missing"      // not found
	PrereqProbeFailed = "probe_failed" // found, but the version probe failed
	PrereqTimeout     = "timeout"      // found, but the version probe timed out

	SourceSystemStatus = "system_status" // GET /api/system/status
	SourceCLICheck     = "cli_check"     // GET /api/system/cli-check
	SourceCLIRecheck   = "cli_recheck"   // the same check, requested by a user re-check
)

func init() {
	eventSpecs[EventPrerequisiteCheck] = map[string]propSpec{
		"tool":   enum(ToolTmux, ToolClaude, ToolCodex),
		"status": enum(PrereqAvailable, PrereqMissing, PrereqProbeFailed, PrereqTimeout),
		"source": enum(SourceSystemStatus, SourceCLICheck, SourceCLIRecheck),
	}
}

var (
	prereqMu   sync.Mutex
	prereqSeen = map[string]bool{}
)

// TrackPrerequisite records one prerequisite observation. It is deduplicated
// for this process run by (tool, status, source), so status polling cannot spam
// events, while a change of status (for example missing then available) is
// reported once. Nothing is remembered while telemetry is off or the build has
// no key, so enabling it later still reports the first observation. No path,
// version output or raw error is ever included.
func TrackPrerequisite(tool, status, source string) {
	if !(posthogKeyPresent() && telemetryEnabled()) {
		return
	}
	spec := eventSpecs[EventPrerequisiteCheck]
	valid := func(key, value string) bool {
		for _, v := range spec[key].enum {
			if v == value {
				return true
			}
		}
		return false
	}
	if !valid("tool", tool) || !valid("status", status) || !valid("source", source) {
		return
	}
	key := tool + "|" + status + "|" + source
	prereqMu.Lock()
	if prereqSeen[key] {
		prereqMu.Unlock()
		return
	}
	prereqSeen[key] = true
	prereqMu.Unlock()
	TrackEvent(EventPrerequisiteCheck, map[string]string{"tool": tool, "status": status, "source": source})
}
