package logparse

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no ansi", "hello world", "hello world"},
		{"csi bold", "\x1B[1mhello\x1B[0m", " hello "},
		{"csi color", "\x1B[32mgreen\x1B[0m", " green "},
		{"osc title", "\x1B]2;My Title\x07rest", " rest"},
		{"osc with ST", "\x1B]2;title\x1B\\rest", " rest"},
		{"mixed", "\x1B[1m\x1B[32mhello\x1B[0m world", "  hello  world"},
		{"control chars", "hello\x07\x08world", "helloworld"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripANSI(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRecentLines(t *testing.T) {
	assert.Empty(t, RecentLines(nil))

	few := []string{"a", "b", "c"}
	assert.Equal(t, few, RecentLines(few))

	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf("line %d", i))
	}
	got := RecentLines(many)
	assert.Len(t, got, 20)
	assert.Equal(t, "line 10", got[0])
	assert.Equal(t, "line 29", got[19])

	// Literal PULSE-like text is ordinary log content, not a protocol event.
	lines := []string{"||PULSE:STATUS Working||", "hello"}
	assert.Equal(t, lines, RecentLines(lines))
}

func TestParseSessionName(t *testing.T) {
	agentType, sessionID := ParseSessionName("claude-550e8400-e29b-41d4-a716-446655440000")
	assert.Equal(t, "claude", agentType)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", sessionID)

	agentType, sessionID = ParseSessionName("gemini-AABB0011-E29B-41D4-A716-446655440000")
	assert.Equal(t, "gemini", agentType)
	assert.Equal(t, "aabb0011-e29b-41d4-a716-446655440000", sessionID)

	agentType, sessionID = ParseSessionName("not-a-coral-session")
	assert.Equal(t, "", agentType)
	assert.Equal(t, "", sessionID)

	agentType, sessionID = ParseSessionName("plain-session-name")
	assert.Equal(t, "", agentType)
	assert.Equal(t, "", sessionID)
}
