// Package logparse provides helpers for reading agent terminal logs: ANSI
// stripping, recent-line tails and tmux session-name parsing.
package logparse

import (
	"regexp"
	"strings"
)

var (
	// ANSI escape sequence regex — handles OSC, CSI, and Fe sequences.
	ansiRE = regexp.MustCompile(
		`\x1B(?:` +
			`\][^\x07\x1B]*(?:\x07|\x1B\\)?` + // OSC sequences
			`|\[[0-?]*[ -/]*[@-~]` + // CSI sequences
			`|[@-Z\\-_]` + // Fe sequences
			`)`)

	// Control character regex for cleanup after ANSI stripping.
	controlCharRE = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

	// UUID regex for parsing tmux session names: {agent_type}-{uuid}
	UUIDSessionRE = regexp.MustCompile(
		`(?i)^(\w+)-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
)

// recentLineCount is how many trailing log lines RecentLines returns.
const recentLineCount = 20

// StripANSI removes ANSI escape sequences and stray control characters from text.
func StripANSI(text string) string {
	text = ansiRE.ReplaceAllString(text, " ")
	text = controlCharRE.ReplaceAllString(text, "")
	return text
}

// RecentLines returns the last lines of a log, oldest first. Lines should
// already be decoded and ANSI-stripped.
func RecentLines(cleanLines []string) []string {
	if len(cleanLines) > recentLineCount {
		cleanLines = cleanLines[len(cleanLines)-recentLineCount:]
	}
	return append([]string(nil), cleanLines...)
}

// ParseSessionName parses a tmux session name in the format "{agent_type}-{uuid}".
// Returns the agent type and session ID, or empty strings if the name doesn't match.
func ParseSessionName(sessionName string) (agentType, sessionID string) {
	m := UUIDSessionRE.FindStringSubmatch(sessionName)
	if m == nil {
		return "", ""
	}
	return m[1], strings.ToLower(m[2])
}
