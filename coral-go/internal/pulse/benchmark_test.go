package pulse

import (
	"strings"
	"testing"
)

// BenchmarkStripANSI tests ANSI stripping performance on large inputs.
// This catches catastrophic regex backtracking (the Python version hit 582s on 4.8MB).
func BenchmarkStripANSI(b *testing.B) {
	// Build a ~1MB string with mixed content and ANSI codes
	var sb strings.Builder
	for i := 0; i < 10000; i++ {
		sb.WriteString("\x1B[32mINFO\x1B[0m: Processing file ")
		sb.WriteString("src/coral/static/app.js")
		sb.WriteString(" with some regular text and \x1B]2;Window Title\x07 embedded OSC\n")
	}
	input := sb.String()
	b.SetBytes(int64(len(input)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripANSI(input)
	}
}

// BenchmarkExtractStatus tests PULSE regex on large non-matching input.
// This is the critical benchmark — the Python PULSE_EVENT_RE with ^.*? caused
// 582 seconds of backtracking on 4.8MB. Go's regex should be linear.
func BenchmarkExtractStatus_NoMatch(b *testing.B) {
	// Build a ~1MB string with no PULSE markers
	var sb strings.Builder
	for i := 0; i < 10000; i++ {
		sb.WriteString("This is a regular log line with no pulse markers, just code output and terminal text\n")
	}
	input := sb.String()
	b.SetBytes(int64(len(input)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractStatus(input)
	}
}

// BenchmarkExtractStatus_WithMatch tests PULSE regex with markers present.
func BenchmarkExtractStatus_WithMatch(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 9999; i++ {
		sb.WriteString("Regular log line without markers\n")
	}
	sb.WriteString("||PULSE:STATUS Building the feature||\n")
	input := sb.String()
	b.SetBytes(int64(len(input)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractStatus(input)
	}
}

// BenchmarkParseLogLines tests the full pipeline on realistic log content.
func BenchmarkParseLogLines(b *testing.B) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, "Regular output line with code and terminal text")
	}
	lines = append(lines, "||PULSE:STATUS Working on feature||")
	lines = append(lines, "||PULSE:SUMMARY Implementing authentication||")
	for i := 0; i < 100; i++ {
		lines = append(lines, "More output after pulse tags")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseLogLines(lines)
	}
}

// BenchmarkRejoinPulseLines tests line rejoining performance.
func BenchmarkRejoinPulseLines(b *testing.B) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, "Regular line without pulse tags")
	}
	// Add some split tags
	lines = append(lines, "||PULSE:SUMMARY Very long summary that spans")
	lines = append(lines, "multiple terminal lines and needs rejoining||")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RejoinPulseLines(lines)
	}
}
