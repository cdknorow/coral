// Package pulse provides parsing for the PULSE agent protocol.
//
// Agents emit structured events using the ||PULSE:<EVENT_TYPE> <payload>|| format.
// This package extracts those events from raw terminal output, handling
// ANSI escape sequences, terminal wrapping, and control characters.
package pulse

import (
	"regexp"
	"strings"
)

// Event types parsed from PULSE protocol.
const (
	EventStatus     = "STATUS"
	EventSummary    = "SUMMARY"
	EventConfidence = "CONFIDENCE"
)

// Event represents a parsed PULSE protocol event.
type Event struct {
	Type    string // STATUS, SUMMARY, or CONFIDENCE
	Payload string // The event payload text
}

// ConfidenceEvent is a parsed CONFIDENCE event with level and reason.
type ConfidenceEvent struct {
	Level  string // "Low" or "High"
	Reason string
}

// Compiled regex patterns matching the Python implementation.
var (
	// ANSI escape sequence regex — handles OSC, CSI, and Fe sequences.
	// This must match the Python ANSI_RE exactly to ensure parity.
	ansiRE = regexp.MustCompile(
		`\x1B(?:` +
			`\][^\x07\x1B]*(?:\x07|\x1B\\)?` + // OSC sequences
			`|\[[0-?]*[ -/]*[@-~]` + // CSI sequences
			`|[@-Z\\-_]` + // Fe sequences
			`)`)

	// Control character regex for cleanup after ANSI stripping.
	controlCharRE = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

	// PULSE event regexes.
	statusRE     = regexp.MustCompile(`\|\|PULSE:STATUS (.*?)\|\|`)
	summaryRE    = regexp.MustCompile(`\|\|PULSE:SUMMARY (.*?)\|\|`)
	confidenceRE = regexp.MustCompile(`\|\|PULSE:CONFIDENCE (Low|High)\s+(.*?)\|\|`)

	// UUID regex for parsing tmux session names: {agent_type}-{uuid}
	UUIDSessionRE = regexp.MustCompile(
		`(?i)^(\w+)-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
)

// StripANSI removes ANSI escape sequences and stray control characters from text.
func StripANSI(text string) string {
	text = ansiRE.ReplaceAllString(text, " ")
	text = controlCharRE.ReplaceAllString(text, "")
	return text
}

// CleanMatch collapses whitespace runs into a single space and strips.
// Returns empty string for template/instruction text containing angle-bracket placeholders.
func CleanMatch(text string) string {
	cleaned := strings.Join(strings.Fields(text), " ")
	// Skip protocol instruction echoes like "Emit a ||PULSE:SUMMARY <your current goal>||"
	if strings.Contains(cleaned, "<") && strings.Contains(cleaned, ">") {
		return ""
	}
	return cleaned
}

// RejoinPulseLines rejoins lines where PULSE tags were split by terminal wrapping.
//
// tmux pipe-pane captures output with hard wraps at the terminal width, which can
// split a single PULSE tag across multiple log lines. This function detects an
// opening ||PULSE: without a closing || on the same line and merges subsequent
// lines until the closing || is found (up to maxJoin continuation lines).
func RejoinPulseLines(lines []string) []string {
	const maxJoin = 5

	var result []string
	var pending string
	depth := 0

	for _, line := range lines {
		if pending != "" {
			pending = pending + " " + strings.TrimSpace(line)
			depth++
			if strings.Contains(line, "||") || depth >= maxJoin {
				result = append(result, pending)
				pending = ""
				depth = 0
			}
		} else if strings.Contains(line, "||PULSE:") {
			idx := strings.LastIndex(line, "||PULSE:")
			rest := line[idx+len("||PULSE:"):]
			if strings.Contains(rest, "||") {
				// Complete tag — emit as-is
				result = append(result, line)
			} else {
				// Incomplete tag — start accumulating
				pending = line
				depth = 0
			}
		} else {
			result = append(result, line)
		}
	}

	// Flush any incomplete tag at end
	if pending != "" {
		result = append(result, pending)
	}

	return result
}

// ExtractStatus extracts the most recent STATUS event from a line.
func ExtractStatus(line string) string {
	matches := statusRE.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return ""
	}
	return CleanMatch(matches[len(matches)-1][1])
}

// ExtractSummary extracts the most recent SUMMARY event from a line.
func ExtractSummary(line string) string {
	matches := summaryRE.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return ""
	}
	return CleanMatch(matches[len(matches)-1][1])
}

// ExtractConfidence extracts a CONFIDENCE event from a line.
func ExtractConfidence(line string) *ConfidenceEvent {
	m := confidenceRE.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	return &ConfidenceEvent{
		Level:  m[1],
		Reason: strings.TrimSpace(m[2]),
	}
}

// LogStatus holds the parsed status information from a log file.
type LogStatus struct {
	Status          string   `json:"status"`
	Summary         string   `json:"summary"`
	StalenessSeconds *float64 `json:"staleness_seconds"`
	RecentLines     []string `json:"recent_lines"`
}

// ParseLogLines extracts status, summary, and recent lines from log content.
// Lines should already be decoded from bytes and ANSI-stripped.
func ParseLogLines(cleanLines []string) *LogStatus {
	result := &LogStatus{}

	// Rejoin split PULSE tags
	cleanLines = RejoinPulseLines(cleanLines)

	// Walk backwards to find latest status and summary
	var recentLines []string
	for i := len(cleanLines) - 1; i >= 0; i-- {
		line := cleanLines[i]

		if result.Status != "" && result.Summary != "" && len(recentLines) >= 20 {
			break
		}

		if result.Status == "" {
			if s := ExtractStatus(line); s != "" {
				result.Status = s
			}
		}

		if result.Summary == "" {
			if s := ExtractSummary(line); s != "" {
				result.Summary = s
			}
		}

		if len(recentLines) < 20 {
			recentLines = append([]string{line}, recentLines...)
		}
	}

	result.RecentLines = recentLines
	return result
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
