// Package agent provides agent implementations for Claude and Gemini.
package agent

// Agent defines the interface for all agent implementations.
type Agent interface {
	// AgentType returns the short identifier (e.g. "claude", "gemini").
	AgentType() string
	// SupportsResume returns whether the agent supports resuming a previous session.
	SupportsResume() bool
	// BuildLaunchCommand builds the shell command to launch this agent.
	BuildLaunchCommand(sessionID string, protocolPath string, resumeSessionID string, flags []string, workingDir string) string
	// PrepareResume prepares for resuming a session (e.g. copy files).
	PrepareResume(sessionID, workingDir string)
	// HistoryBasePath returns the root directory for history files.
	HistoryBasePath() string
	// HistoryGlobPattern returns the glob pattern for history files.
	HistoryGlobPattern() string
}

// GetAgent returns the agent implementation for the given type.
func GetAgent(agentType string) Agent {
	switch agentType {
	case "gemini":
		return &GeminiAgent{}
	default:
		return &ClaudeAgent{}
	}
}

// GetAllAgents returns all registered agent implementations.
func GetAllAgents() []Agent {
	return []Agent{&ClaudeAgent{}, &GeminiAgent{}}
}
