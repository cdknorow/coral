package pulse

import (
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

func TestCleanMatch(t *testing.T) {
	assert.Equal(t, "hello world", CleanMatch("  hello   world  "))
	assert.Equal(t, "", CleanMatch("Emit ||PULSE:SUMMARY <your current goal>||"))
	assert.Equal(t, "real status update", CleanMatch("real status update"))
}

func TestExtractStatus(t *testing.T) {
	assert.Equal(t, "Building feature X",
		ExtractStatus("some output ||PULSE:STATUS Building feature X|| more output"))
	assert.Equal(t, "", ExtractStatus("no pulse here"))
	assert.Equal(t, "", ExtractStatus("||PULSE:STATUS <your current task>||"))
}

func TestExtractSummary(t *testing.T) {
	assert.Equal(t, "Implementing auth system",
		ExtractSummary("||PULSE:SUMMARY Implementing auth system||"))
	assert.Equal(t, "", ExtractSummary("no summary"))
}

func TestExtractConfidence(t *testing.T) {
	c := ExtractConfidence("||PULSE:CONFIDENCE Low unsure about the API design||")
	assert.NotNil(t, c)
	assert.Equal(t, "Low", c.Level)
	assert.Equal(t, "unsure about the API design", c.Reason)

	c = ExtractConfidence("||PULSE:CONFIDENCE High tests are passing||")
	assert.NotNil(t, c)
	assert.Equal(t, "High", c.Level)

	assert.Nil(t, ExtractConfidence("no confidence here"))
}

func TestRejoinPulseLines(t *testing.T) {
	t.Run("complete tag passes through", func(t *testing.T) {
		lines := []string{
			"some output",
			"||PULSE:STATUS Building feature X||",
			"more output",
		}
		result := RejoinPulseLines(lines)
		assert.Equal(t, lines, result)
	})

	t.Run("split tag is rejoined", func(t *testing.T) {
		lines := []string{
			"||PULSE:SUMMARY Moving Settings button to top gear icon and creating",
			"persistent settings store in database||",
		}
		result := RejoinPulseLines(lines)
		assert.Len(t, result, 1)
		assert.Contains(t, result[0], "||PULSE:SUMMARY")
		assert.Contains(t, result[0], "persistent settings store in database||")
	})

	t.Run("max join limit", func(t *testing.T) {
		lines := []string{
			"||PULSE:SUMMARY Very long summary that spans",
			"line 1",
			"line 2",
			"line 3",
			"line 4",
			"line 5",
			"line 6 should not be joined",
		}
		result := RejoinPulseLines(lines)
		// After max join (5), the pending is flushed, plus line 6 and 7
		assert.True(t, len(result) >= 2)
	})

	t.Run("non-pulse lines pass through", func(t *testing.T) {
		lines := []string{"line 1", "line 2", "line 3"}
		result := RejoinPulseLines(lines)
		assert.Equal(t, lines, result)
	})
}

func TestParseLogLines(t *testing.T) {
	lines := []string{
		"Starting up...",
		"||PULSE:SUMMARY Implementing user auth||",
		"Working on login flow...",
		"||PULSE:STATUS Writing login handler||",
		"More work...",
	}
	result := ParseLogLines(lines)
	assert.Equal(t, "Writing login handler", result.Status)
	assert.Equal(t, "Implementing user auth", result.Summary)
	assert.Len(t, result.RecentLines, 5)
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
