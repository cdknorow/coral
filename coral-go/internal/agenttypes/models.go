package agenttypes

// AgentTypeModels is the canonical per-agent-type list of model identifiers
// surfaced to the frontend as dropdown choices. Users can still type any
// free-form model string in the UI — this list is only the curated set.
//
// Entries are ordered: preferred/default model first.
var AgentTypeModels = map[string][]string{
	Claude: {
		"claude-opus-5-5",
		"claude-sonnet-5-5",
		"claude-haiku-5-5",
		"claude-fable-5-1",
	},
	Codex: {},
	Agy: {
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-medium",
		"gemini-3.7-flash-high",
		"gemini-3.1-pro-high",
		"claude-sonnet-4-6",
	},
	Gemini: {
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-medium",
		"gemini-3.7-flash-high",
		"gemini-3.1-pro-high",
		"claude-sonnet-4-6",
	},
	Terminal: {},
}
