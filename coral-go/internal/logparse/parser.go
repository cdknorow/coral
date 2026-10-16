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

// StripANSI removes ANSI escape sequences and stray control characters from text: each
// sequence becomes one space, and the control characters left (an ESC that starts no
// sequence included) are dropped.
//
// One pass over the bytes (#3711): it ran as two regexp ReplaceAllString passes, and on a
// poll's every agent log that was the server's hottest path. The output is byte for byte the
// regexps' (stripANSIRegexp, kept for the differential test): an OSC is ESC ] up to BEL or ESC
// with an optional BEL or ESC \ terminator; a CSI is ESC [, parameter bytes 0x30-0x3F,
// intermediate bytes 0x20-0x2F and a final byte 0x40-0x7E, else no sequence; an Fe is ESC and
// one byte in 0x40-0x5A or 0x5C-0x5F. The classes are disjoint, so there is nothing to
// backtrack, and no delimiter byte occurs inside a UTF-8 sequence, so bytes and runes agree.
func StripANSI(text string) string {
	clean := true
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	n := len(text)
	for i := 0; i < n; {
		c := text[i]
		if c == 0x1b {
			if end := escEnd(text, i); end > i {
				b.WriteByte(' ')
				i = end
				continue
			}
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
			i++ // a control character (an ESC that starts no sequence included)
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// escEnd is where the escape sequence starting at text[i] (an ESC) ends, or i when it
// starts none.
func escEnd(text string, i int) int {
	n := len(text)
	if i+1 >= n {
		return i
	}
	switch c := text[i+1]; {
	case c == ']': // OSC
		j := i + 2
		for j < n && text[j] != 0x07 && text[j] != 0x1b {
			j++
		}
		if j < n && text[j] == 0x07 {
			return j + 1
		}
		if j+1 < n && text[j] == 0x1b && text[j+1] == '\\' {
			return j + 2
		}
		return j
	case c == '[': // CSI
		j := i + 2
		for j < n && text[j] >= 0x30 && text[j] <= 0x3f {
			j++
		}
		for j < n && text[j] >= 0x20 && text[j] <= 0x2f {
			j++
		}
		if j < n && text[j] >= 0x40 && text[j] <= 0x7e {
			return j + 1
		}
		return i // '[' is no Fe byte either
	case c >= 0x40 && c <= 0x5a || c >= 0x5c && c <= 0x5f: // Fe
		return i + 2
	}
	return i
}

// stripANSIRegexp is StripANSI as it was (two regexp passes), for the differential test.
func stripANSIRegexp(text string) string {
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
