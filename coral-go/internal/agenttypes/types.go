// Package agenttypes defines agent type constants shared across packages.
// This package has no dependencies and can be imported by any other package
// without risk of circular imports.
package agenttypes

// Agent type identifiers.
const (
	Claude   = "claude"
	Gemini   = "gemini"
	Codex    = "codex"
	Pi       = "pi"
	Terminal = "terminal"
)

// CoralSessionMarkerPrefix prefixes a session marker injected into per-agent
// instructions. History parsers use it to map agent-native transcripts back to
// Coral's stable live session IDs.
const CoralSessionMarkerPrefix = "CORAL_SESSION_ID:"
