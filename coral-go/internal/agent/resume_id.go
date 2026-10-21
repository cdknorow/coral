package agent

import (
	"github.com/google/uuid"
	at "github.com/subgentic/coral/internal/agenttypes"
)

// IsResumeID reports whether id is a resume session id safe to put in a file path for agentType
// (#3582). A resume id comes from the request body (Resume, launch resume_session_id) and is
// joined into history and session-directory paths; a value like "a/../../b" or "/abs" would
// escape.
//
// Two rules, by what each agent really stores (checked against the operator's sessions.db):
//   - Claude, agy, gemini, terminal and pi use Coral-generated session ids, always the canonical
//     36-character UUID, so they get the strict UUID rule.
//   - Codex keys resume on its own native thread ids, which are not UUIDs (up to 64 chars of
//     [A-Za-z0-9_-]), so it — and any unknown or unset type — gets the safe-charset rule.
//
// The safe-charset rule is the real anti-traversal boundary and is applied to every id first: it
// admits no path separator, "." or control character, so no id of any type can escape. The UUID
// rule is an extra, stricter check for the types that always use UUIDs.
func IsResumeID(agentType, id string) bool {
	if !safeResumeCharset(id) {
		return false
	}
	// Resolve through GetAgent so the rule matches the agent that actually launches: an empty or
	// unknown type is Claude there, so it gets the UUID rule, not the loose one (Backend's MUST 2
	// on #3582). Only Codex keys resume on non-UUID native ids.
	switch GetAgent(agentType).AgentType() {
	case at.Codex:
		return true
	default: // claude (incl. "" and unknown), agy, gemini, terminal, pi: always UUIDs
		return isCanonicalUUID(id)
	}
}

// safeResumeCharset admits a non-empty id of at most 128 bytes made only of ASCII letters,
// digits, '-' and '_': no '/', '.', ".." or control character, so it can't escape a path. The
// first character must be a letter or digit, so an id can never be read as a command-line option
// (for example "-x" or "--help"); no real id starts with '-' or '_' (checked in sessions.db).
func safeResumeCharset(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if c := id[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// isCanonicalUUID requires the canonical 36-character hyphenated form (what Coral generates),
// rejecting the non-canonical forms uuid.Parse would otherwise accept (urn:, braces, no hyphens)
// so the stored name stays regular.
func isCanonicalUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
