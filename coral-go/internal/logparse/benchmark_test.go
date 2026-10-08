package logparse

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
