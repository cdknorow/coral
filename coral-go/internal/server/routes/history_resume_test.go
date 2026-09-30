package routes

import "testing"

func TestNativeResumeIDPreservesProviderIdentity(t *testing.T) {
	tests := []struct {
		name, agentType, sourceFile, fallback, want string
	}{
		{"codex marker maps to rollout", "codex", "/codex/sessions/2026/09/30/rollout-2026-09-30T12-00-00-coral.jsonl", "coral-id", "rollout-2026-09-30T12-00-00-coral"},
		{"antigravity marker maps to conversation", "agy", "/brain/native-conversation/.system_generated/logs/transcript.jsonl", "coral-id", "native-conversation"},
		{"claude keeps indexed ID", "claude", "/claude/project/coral-id.jsonl", "coral-id", "coral-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeResumeID(tt.agentType, tt.sourceFile, tt.fallback); got != tt.want {
				t.Fatalf("nativeResumeID() = %q, want %q", got, tt.want)
			}
		})
	}
}
