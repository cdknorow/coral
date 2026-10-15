package agent

import "github.com/google/uuid"

// IsResumeID reports whether id is a canonical session UUID, safe to put in a file path (#3582).
// A resume session id comes from the request body (Resume, launch resume_session_id) and is
// joined into history and session-directory paths; a value like "a/../../b" or "/abs" would
// escape. Requiring the canonical 36-character hyphenated form (what Coral generates) rejects
// every such value, path separators and "." among them, and also the non-canonical forms
// uuid.Parse would otherwise accept (urn:, braces, no hyphens) so the stored name stays regular.
func IsResumeID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
