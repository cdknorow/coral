package background

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"DevOps Engineer", "devops-engineer"},
		{"Artist", "artist"},
		{"QA Tester", "qa-tester"},
		{"MyAgent_v2", "myagent_v2"},
		{"Hello World!", "hello-world"},
		{"  spaces  ", "--spaces--"},
		{"UPPERCASE", "uppercase"},
		{"agent-123", "agent-123"},
	}
	for _, tt := range tests {
		got := Slugify(tt.input)
		if got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWriteAndReadKnowledgeFile(t *testing.T) {
	dir := t.TempDir()
	content := `---
type: Agent Knowledge
title: DevOps
description: Test knowledge
tags: [devops]
timestamp: 2026-01-01T00:00:00Z
---

# DevOps

## Tools & Commands
- kubectl apply -f deploy.yaml
- docker build -t myapp .
`

	_, err := WriteKnowledgeFile(dir, "my-team", "DevOps", content)
	if err != nil {
		t.Fatalf("WriteKnowledgeFile: %v", err)
	}

	// Check file exists
	path := filepath.Join(dir, "agent_knowledge", "my-team", "devops.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content mismatch:\ngot: %s\nwant: %s", string(data), content)
	}

	// Test LoadAgentKnowledge
	got := LoadAgentKnowledge(dir, "my-team", "DevOps")
	if got != content {
		t.Errorf("LoadAgentKnowledge mismatch:\ngot: %s\nwant: %s", got, content)
	}

	// Test with non-existent agent
	got = LoadAgentKnowledge(dir, "my-team", "NonExistent")
	if got != "" {
		t.Errorf("LoadAgentKnowledge for non-existent agent should return empty, got: %s", got)
	}
}

func TestReadKnowledgeBundle(t *testing.T) {
	dir := t.TempDir()

	// No bundle yet
	bundle, err := ReadKnowledgeBundle(dir, "no-team")
	if err != nil {
		t.Fatalf("ReadKnowledgeBundle: %v", err)
	}
	if bundle != nil {
		t.Error("expected nil bundle for non-existent team")
	}

	// Create a bundle
	bundleDir := filepath.Join(dir, "agent_knowledge", "test-team")
	os.MkdirAll(bundleDir, 0755)

	indexContent := `---
type: Team Knowledge Bundle
title: test-team
timestamp: 2026-01-01T00:00:00Z
---

# test-team — Team Knowledge
`
	os.WriteFile(filepath.Join(bundleDir, "index.md"), []byte(indexContent), 0644)
	os.WriteFile(filepath.Join(bundleDir, "developer.md"), []byte("# Developer knowledge"), 0644)
	os.WriteFile(filepath.Join(bundleDir, "artist.md"), []byte("# Artist knowledge"), 0644)

	bundle, err = ReadKnowledgeBundle(dir, "test-team")
	if err != nil {
		t.Fatalf("ReadKnowledgeBundle: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	if bundle.IndexMD != indexContent {
		t.Errorf("index content mismatch")
	}
	if len(bundle.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(bundle.Agents))
	}
	if bundle.Agents["developer"] == nil {
		t.Error("missing developer agent")
	}
	if bundle.Agents["artist"] == nil {
		t.Error("missing artist agent")
	}
	if bundle.Agents["developer"].FinalMD != "# Developer knowledge" {
		t.Errorf("developer content mismatch: %s", bundle.Agents["developer"].FinalMD)
	}
}

func TestAssembleOKFFallback(t *testing.T) {
	result := assembleOKFFallback("DevOps", "worker", "my-team", "2026-01-01T00:00:00Z", "## Tools\n- kubectl", "## Collab\n- works with Artist")

	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Check it has OKF frontmatter
	if !contains(result, "type: Agent Knowledge") {
		t.Error("missing OKF type field")
	}
	if !contains(result, "title: DevOps") {
		t.Error("missing title")
	}
	if !contains(result, "timestamp: 2026-01-01") {
		t.Error("missing timestamp")
	}
	if !contains(result, "## Tools") {
		t.Error("missing pass1 content")
	}
	if !contains(result, "## Collaboration") {
		t.Error("missing collaboration section")
	}
}

func TestParseCollabLinks(t *testing.T) {
	agents := []agentMeta{
		{name: "DevOps", role: "worker"},
		{name: "Artist", role: "worker"},
		{name: "QA", role: "worker"},
	}

	collabText := `## Collaboration Map
- DevOps → Artist: deploys assets after Artist generates them
- QA depends on DevOps: runs tests after deployment
- Artist hands off to QA: provides mockups for visual testing
`

	links := parseCollabLinks(collabText, agents)
	if len(links) < 2 {
		t.Errorf("expected at least 2 links, got %d", len(links))
	}

	// Verify at least one link has the right structure
	foundDevOpsArtist := false
	for _, l := range links {
		if l.From != "" && l.To != "" && l.From != l.To {
			foundDevOpsArtist = true
		}
	}
	if !foundDevOpsArtist {
		t.Error("expected at least one valid cross-agent link")
	}
}

func TestExtractAgentName(t *testing.T) {
	known := map[string]bool{
		"devops": true,
		"artist": true,
		"qa":     true,
	}

	tests := []struct {
		text string
		want string
	}{
		{"DevOps", "DevOps"},
		{"the Artist agent", "Artist"},
		{"QA runs tests", "QA"},
		{"Unknown agent", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractAgentName(tt.text, known)
		if got != tt.want {
			t.Errorf("extractAgentName(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestWriteKnowledgeBundle(t *testing.T) {
	dir := t.TempDir()

	bundle := &KnowledgeBundle{
		TeamName:  "test-team",
		CreatedAt: "2026-01-01T00:00:00Z",
		IndexMD:   "# Index",
		Agents: map[string]*AgentConcept{
			"dev": {AgentName: "dev", FinalMD: "# Dev knowledge"},
			"qa":  {AgentName: "qa", FinalMD: "# QA knowledge"},
		},
	}

	err := writeKnowledgeBundle(dir, bundle)
	if err != nil {
		t.Fatalf("writeKnowledgeBundle: %v", err)
	}

	// Verify files
	indexPath := filepath.Join(dir, "agent_knowledge", "test-team", "index.md")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("index.md not created: %v", err)
	}
	if string(data) != "# Index" {
		t.Errorf("index content mismatch: %s", string(data))
	}

	devPath := filepath.Join(dir, "agent_knowledge", "test-team", "dev.md")
	data, err = os.ReadFile(devPath)
	if err != nil {
		t.Fatalf("dev.md not created: %v", err)
	}
	if string(data) != "# Dev knowledge" {
		t.Errorf("dev content mismatch: %s", string(data))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// fakeKnowledgeCall answers the three passes; an agent named in failExtract has its
// extraction fail, and failCollab fails the cross-referencing pass.
func fakeKnowledgeCall(failExtract map[string]bool, failCollab bool) func(prompt, content string) (string, error) {
	return func(prompt, content string) (string, error) {
		switch {
		case strings.HasPrefix(prompt, "You are an agent knowledge extractor"):
			for name := range failExtract {
				if strings.Contains(prompt, `name is "`+name+`"`) {
					return "", errors.New("claude CLI failed: exit status 1: rate limited")
				}
			}
			return "## Tools & Commands\nmake test", nil
		case strings.HasPrefix(prompt, "You are analyzing collaboration patterns"):
			if failCollab {
				return "", errors.New("claude CLI timed out after 2m0s")
			}
			return "## Collaboration Map\nnone", nil
		}
		return "---\ntype: Agent Knowledge\n---\nnew knowledge", nil
	}
}

func distillTestAgents() []agentMeta {
	return []agentMeta{
		{name: "Ada", role: "worker", transcript: "ada transcript"},
		{name: "Bob", role: "worker", transcript: "bob transcript"},
		{name: "Cy", role: "worker"}, // no transcript: not a failure
	}
}

func newTestBundle(team string) *KnowledgeBundle {
	return &KnowledgeBundle{TeamName: team, Agents: map[string]*AgentConcept{}}
}

// QA's #3325 note 2: an agent whose extraction fails is reported, keeps its earlier
// knowledge, and its error text never ends up in any agent's document.
func TestDistillReportsFailedAgentsAndKeepsTheirKnowledge(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteKnowledgeFile(dir, "team", "Ada", "OLD ADA")
	require.NoError(t, err)
	var phases []string
	b, err := distillAgents(newTestBundle("team"), distillTestAgents(), fakeKnowledgeCall(map[string]bool{"Ada": true}, false), dir,
		func(p DistillProgress) { phases = append(phases, p.Phase) })
	require.NoError(t, err)
	require.Equal(t, []DistillFailure{{Agent: "Ada", Error: "claude CLI failed: exit status 1: rate limited"}}, b.FailedAgents)
	assert.Empty(t, b.CollaborationError)
	assert.Equal(t, "complete", phases[len(phases)-1])

	assert.Equal(t, "OLD ADA", LoadAgentKnowledge(dir, "team", "Ada"), "the failed agent keeps its earlier knowledge")
	assert.Contains(t, LoadAgentKnowledge(dir, "team", "Bob"), "new knowledge")
	assert.Contains(t, LoadAgentKnowledge(dir, "team", "Cy"), "new knowledge")
	read, err := ReadKnowledgeBundle(dir, "team")
	require.NoError(t, err)
	assert.Contains(t, read.IndexMD, "[Ada](./ada.md)", "listed: it has earlier knowledge")
	for name, c := range read.Agents {
		assert.NotContains(t, c.FinalMD, "rate limited", name)
	}

	// Without earlier knowledge, the failed agent gets no file and no index entry.
	dir2 := t.TempDir()
	_, err = distillAgents(newTestBundle("team"), distillTestAgents(), fakeKnowledgeCall(map[string]bool{"Ada": true}, false), dir2, nil)
	require.NoError(t, err)
	assert.Empty(t, LoadAgentKnowledge(dir2, "team", "Ada"))
	read, err = ReadKnowledgeBundle(dir2, "team")
	require.NoError(t, err)
	assert.NotContains(t, read.IndexMD, "ada.md")
	assert.Contains(t, read.IndexMD, "[Bob](./bob.md)")
}

// When every extraction fails there is nothing new: the run fails and the team's earlier
// knowledge is left exactly as it was.
func TestDistillFailsWhenEveryExtractionFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteKnowledgeIndex(dir, "team", "OLD INDEX"))
	_, err := WriteKnowledgeFile(dir, "team", "Bob", "OLD BOB")
	require.NoError(t, err)
	_, err = distillAgents(newTestBundle("team"), distillTestAgents(), fakeKnowledgeCall(map[string]bool{"Ada": true, "Bob": true}, false), dir, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent's knowledge could be extracted (2 of 2 failed): claude CLI failed")
	read, err := ReadKnowledgeBundle(dir, "team")
	require.NoError(t, err)
	assert.Equal(t, "OLD INDEX", read.IndexMD)
	assert.Equal(t, "OLD BOB", LoadAgentKnowledge(dir, "team", "Bob"))
	assert.Empty(t, LoadAgentKnowledge(dir, "team", "Cy"), "nothing written")
}

// A failed cross-referencing pass is reported; the documents are built without it, and
// its error text is not in them.
func TestDistillReportsAFailedCollaborationPass(t *testing.T) {
	dir := t.TempDir()
	b, err := distillAgents(newTestBundle("team"), distillTestAgents(), fakeKnowledgeCall(nil, true), dir, nil)
	require.NoError(t, err)
	assert.Empty(t, b.FailedAgents)
	assert.Equal(t, "claude CLI timed out after 2m0s", b.CollaborationError)
	for name, c := range b.Agents {
		assert.NotContains(t, c.Pass2Raw, "timed out", name)
	}
}
