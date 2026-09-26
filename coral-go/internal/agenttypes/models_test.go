package agenttypes

import "testing"

func TestAgentTypeModelsShape(t *testing.T) {
	for _, key := range []string{Claude, Codex, Agy, Gemini, Terminal} {
		if _, ok := AgentTypeModels[key]; !ok {
			t.Errorf("AgentTypeModels missing entry for %q", key)
		}
	}
}

func TestAgentTypeModelsClaudePopulated(t *testing.T) {
	if len(AgentTypeModels[Claude]) < 1 {
		t.Fatalf("expected at least one Claude model, got %d", len(AgentTypeModels[Claude]))
	}
}

func TestAgentTypeModelsAgyPopulated(t *testing.T) {
	if len(AgentTypeModels[Agy]) < 1 {
		t.Fatalf("expected at least one Agy model, got %d", len(AgentTypeModels[Agy]))
	}
}
