package routes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectKnowledgeIntoPrompt_NoKnowledge(t *testing.T) {
	dir := t.TempDir()
	prompt := "You are a DevOps engineer."
	result := InjectKnowledgeIntoPrompt(dir, "my-team", "DevOps", prompt)
	if result != prompt {
		t.Errorf("expected unchanged prompt, got: %s", result)
	}
}

func TestInjectKnowledgeIntoPrompt_WithKnowledge(t *testing.T) {
	dir := t.TempDir()

	// Create knowledge file
	knowledgeDir := filepath.Join(dir, "agent_knowledge", "my-team")
	os.MkdirAll(knowledgeDir, 0755)
	knowledge := "# DevOps Knowledge\n\n## Tools\n- kubectl\n- docker"
	os.WriteFile(filepath.Join(knowledgeDir, "devops.md"), []byte(knowledge), 0644)

	prompt := "You are a DevOps engineer."
	result := InjectKnowledgeIntoPrompt(dir, "my-team", "DevOps", prompt)

	if !strings.HasPrefix(result, prompt) {
		t.Error("result should start with original prompt")
	}
	if !strings.Contains(result, "Institutional Knowledge") {
		t.Error("result should contain knowledge header")
	}
	if !strings.Contains(result, "kubectl") {
		t.Error("result should contain knowledge content")
	}
}

func TestInjectKnowledgeIntoPrompt_EmptyPrompt(t *testing.T) {
	dir := t.TempDir()

	knowledgeDir := filepath.Join(dir, "agent_knowledge", "my-team")
	os.MkdirAll(knowledgeDir, 0755)
	os.WriteFile(filepath.Join(knowledgeDir, "artist.md"), []byte("# Artist tools"), 0644)

	result := InjectKnowledgeIntoPrompt(dir, "my-team", "Artist", "")
	if !strings.Contains(result, "Artist tools") {
		t.Error("result should contain knowledge even with empty prompt")
	}
}
