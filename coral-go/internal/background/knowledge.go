package background

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/store"
)

// KnowledgeBundle holds the OKF output for a team.
type KnowledgeBundle struct {
	TeamName  string                    `json:"team_name"`
	CreatedAt string                    `json:"created_at"`
	Agents    map[string]*AgentConcept  `json:"agents"`
	Links     []KnowledgeLink           `json:"links"`
	IndexMD   string                    `json:"index_md"`
}

// AgentConcept is a single OKF concept file for an agent.
type AgentConcept struct {
	AgentName string `json:"agent_name"`
	Role      string `json:"role"`
	Pass1Raw  string `json:"pass1_raw"`
	Pass2Raw  string `json:"pass2_raw"`
	FinalMD   string `json:"final_md"`
}

// agentMeta holds agent info during distillation.
type agentMeta struct {
	name       string
	role       string
	transcript string
}

// KnowledgeLink represents a cross-reference between agents.
type KnowledgeLink struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Relation    string `json:"relation"`
	Description string `json:"description"`
}

// DistillProgress tracks the progress of a distillation run.
type DistillProgress struct {
	Phase     string `json:"phase"`
	Agent     string `json:"agent,omitempty"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Error     string `json:"error,omitempty"`
}

const maxKnowledgeTranscriptChars = 50000

const pass1Prompt = `You are an agent knowledge extractor. You will be given the full conversation transcript of an AI coding agent that works on a team. The agent's name is "%s" and their role is "%s".

Extract the institutional knowledge this agent has built up. Output markdown with EXACTLY these sections:

## Tools & Commands
What CLI tools, scripts, APIs, or commands does this agent use regularly? Include exact command patterns, flags, and workflows they've developed.

## Processes & Procedures
What step-by-step processes does this agent follow? (e.g., release procedures, testing workflows, build steps, deployment sequences). Be specific — include the actual steps.

## Keys, Configs & Environment
What API keys, environment variables, config files, or credentials does this agent reference? Include variable names and where they're sourced from (DO NOT include actual secret values — just the names and locations).

## Domain Knowledge
What domain-specific knowledge has this agent accumulated? Technical decisions, architectural patterns, gotchas, workarounds, or things they've learned the hard way.

## Working Patterns
How does this agent prefer to work? What approaches have they found effective? What do they avoid?

Be concrete and specific — extract actual commands, file paths, and procedures, not vague summaries. If a section has no relevant content, write "None observed." Keep total output under 800 words.`

const pass2Prompt = `You are analyzing collaboration patterns across a team of AI agents. Below are the individual knowledge extracts for each agent on the team "%s".

For each agent, analyze how they interact with and depend on other agents. Output markdown with these sections:

## Collaboration Map
For each agent, list:
- Who they communicate with and why
- What they hand off to other agents
- What they receive from other agents
- Shared resources or workflows

## Handoff Protocols
Document any established patterns for passing work between agents (e.g., "DevOps signals Artist when a deploy completes", "QA blocks on Dev finishing the PR").

## Shared Resources
List any files, branches, APIs, tools, or systems that multiple agents touch. Note any coordination needed.

## Conflict Zones
Areas where agents might step on each other — shared files, competing git operations, resource contention.

Keep it concrete and actionable. Under 500 words total.

---

%s`

const pass3Prompt = `You are converting agent knowledge into Open Knowledge Format (OKF) documents. OKF uses markdown files with YAML frontmatter where the only required field is "type". Links between concepts use standard markdown links.

I will give you the extracted knowledge for agent "%s" (role: %s) on team "%s", along with collaboration patterns.

Convert this into a single OKF markdown document with this structure:

---
type: Agent Knowledge
title: %s
description: Institutional knowledge for %s on team %s
tags: [%s]
timestamp: %s
---

Then write the body using the knowledge provided. Organize it clearly with ## headings. Where this agent references other agents, tools, or shared resources, use markdown links in the format [concept name](../concept-file.md). For example, if this agent hands work to "Artist", link it as [Artist](./artist.md).

Keep the content factual and actionable — this document will be injected into the agent's context when it restarts, so it should help the agent pick up where it left off.

Here is the agent's extracted knowledge:

%s

Here are the team collaboration patterns:

%s`

func inferRole(prompt string) string {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "orchestrat") || strings.Contains(lower, "coordinator") {
		return "orchestrator"
	}
	return "worker"
}

// DistillTeamKnowledge runs the three-pass knowledge distillation pipeline for a team.
func DistillTeamKnowledge(ctx context.Context, ss *store.SessionStore, ts *store.TeamStore, teamName, coralDir string, progressFn func(DistillProgress)) (*KnowledgeBundle, error) {
	claudePath, err := checkClaudeCLI(ctx)
	if err != nil {
		return nil, err
	}

	settings, _ := ss.GetSettings(ctx)
	callTimeout := knowledgeCallTimeoutFromSettings(settings)

	bundle := &KnowledgeBundle{
		TeamName:  teamName,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Agents:    make(map[string]*AgentConcept),
	}

	// Collect agent info and transcripts — try team store first, fall back to live sessions
	var agents []agentMeta

	team, _ := ts.GetTeam(ctx, teamName)
	if team != nil && len(team.Members) > 0 {
		for _, m := range team.Members {
			var cfg struct {
				Prompt string `json:"prompt"`
			}
			json.Unmarshal([]byte(m.AgentConfigJSON), &cfg)

			role := inferRole(cfg.Prompt)
			transcript := ""
			if m.SessionID != nil {
				t, err := loadTranscript(ctx, ss, *m.SessionID)
				if err == nil && t != "" {
					transcript = t
				}
			}

			agents = append(agents, agentMeta{
				name:       m.AgentName,
				role:       role,
				transcript: transcript,
			})
		}
	} else {
		// Fallback: look up live sessions on this board
		liveSessions, err := ss.GetBoardSessions(ctx, teamName)
		if err != nil || len(liveSessions) == 0 {
			return nil, fmt.Errorf("team %q not found", teamName)
		}
		for _, ls := range liveSessions {
			name := ls.AgentName
			if ls.DisplayName != nil && *ls.DisplayName != "" {
				name = *ls.DisplayName
			}
			prompt := ""
			if ls.Prompt != nil {
				prompt = *ls.Prompt
			}
			role := inferRole(prompt)
			transcript, _ := loadTranscript(ctx, ss, ls.SessionID)

			agents = append(agents, agentMeta{
				name:       name,
				role:       role,
				transcript: transcript,
			})
		}
	}

	total := len(agents)
	if total == 0 {
		return nil, fmt.Errorf("team %q has no members", teamName)
	}

	// ── Pass 1: Individual agent knowledge extraction ──
	for i, a := range agents {
		if progressFn != nil {
			progressFn(DistillProgress{Phase: "extracting", Agent: a.name, Total: total, Completed: i})
		}

		concept := &AgentConcept{
			AgentName: a.name,
			Role:      a.role,
		}

		if a.transcript == "" {
			concept.Pass1Raw = "No session transcript available for this agent."
		} else {
			prompt := fmt.Sprintf(pass1Prompt, a.name, a.role)
			result, err := callClaudeForKnowledge(ctx, claudePath, callTimeout, prompt, a.transcript)
			if err != nil {
				concept.Pass1Raw = fmt.Sprintf("*Knowledge extraction failed: %v*", err)
			} else {
				concept.Pass1Raw = result
			}
		}

		bundle.Agents[a.name] = concept
	}

	// ── Pass 2: Cross-reference collaboration patterns ──
	if progressFn != nil {
		progressFn(DistillProgress{Phase: "cross-referencing", Total: total, Completed: total})
	}

	var agentSummaries strings.Builder
	for _, a := range agents {
		concept := bundle.Agents[a.name]
		fmt.Fprintf(&agentSummaries, "### Agent: %s (Role: %s)\n\n%s\n\n---\n\n", a.name, a.role, concept.Pass1Raw)
	}

	collabPrompt := fmt.Sprintf(pass2Prompt, teamName, agentSummaries.String())
	collabResult, err := callClaudeForKnowledge(ctx, claudePath, callTimeout, collabPrompt, "")
	if err != nil {
		collabResult = fmt.Sprintf("*Collaboration analysis failed: %v*", err)
	}

	// Store pass2 on all agents
	for _, concept := range bundle.Agents {
		concept.Pass2Raw = collabResult
	}

	// Parse links from collaboration result
	bundle.Links = parseCollabLinks(collabResult, agents)

	// ── Pass 3: Convert to OKF format ──
	now := time.Now().UTC().Format(time.RFC3339)
	for i, a := range agents {
		if progressFn != nil {
			progressFn(DistillProgress{Phase: "formatting", Agent: a.name, Total: total, Completed: i})
		}

		concept := bundle.Agents[a.name]
		slug := slugify(a.name)
		tags := fmt.Sprintf("%s, %s, %s", a.role, teamName, slug)

		prompt := fmt.Sprintf(pass3Prompt,
			a.name, a.role, teamName,
			a.name, a.name, teamName,
			tags, now,
			concept.Pass1Raw, collabResult)

		result, err := callClaudeForKnowledge(ctx, claudePath, callTimeout, prompt, "")
		if err != nil {
			// Fallback: assemble OKF manually from pass1+pass2
			concept.FinalMD = assembleOKFFallback(a.name, a.role, teamName, now, concept.Pass1Raw, collabResult)
		} else {
			concept.FinalMD = result
		}
	}

	// Build index.md
	bundle.IndexMD = buildIndexMD(teamName, now, agents, bundle)

	// Write to disk
	if err := writeKnowledgeBundle(coralDir, bundle); err != nil {
		return bundle, fmt.Errorf("knowledge distilled but failed to write: %w", err)
	}

	if progressFn != nil {
		progressFn(DistillProgress{Phase: "complete", Total: total, Completed: total})
	}

	return bundle, nil
}

// ReadKnowledgeBundle reads an existing knowledge bundle from disk.
func ReadKnowledgeBundle(coralDir, teamName string) (*KnowledgeBundle, error) {
	dir := filepath.Join(coralDir, "agent_knowledge", teamName)
	indexPath := filepath.Join(dir, "index.md")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil, nil
	}

	bundle := &KnowledgeBundle{
		TeamName: teamName,
		Agents:   make(map[string]*AgentConcept),
	}

	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	bundle.IndexMD = string(indexData)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() || e.Name() == "index.md" || e.Name() == "collaboration.md" || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		bundle.Agents[name] = &AgentConcept{
			AgentName: name,
			FinalMD:   string(data),
		}
	}

	return bundle, nil
}

// LoadAgentKnowledge reads the OKF knowledge file for a specific agent on a team.
func LoadAgentKnowledge(coralDir, teamName, agentName string) string {
	slug := slugify(agentName)
	path := filepath.Join(coralDir, "agent_knowledge", teamName, slug+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// WriteKnowledgeFile writes a single agent's knowledge file to disk.
func WriteKnowledgeFile(coralDir, teamName, agentName, content string) error {
	dir := filepath.Join(coralDir, "agent_knowledge", teamName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	slug := slugify(agentName)
	return os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0644)
}

// WriteKnowledgeIndex writes the team's index.md knowledge file.
func WriteKnowledgeIndex(coralDir, teamName, content string) error {
	dir := filepath.Join(coralDir, "agent_knowledge", teamName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.md"), []byte(content), 0644)
}

const (
	// defaultKnowledgeCallTimeout is the maximum time a single Claude CLI
	// call may run during knowledge distillation.
	defaultKnowledgeCallTimeout = 2 * time.Minute

	// KnowledgeTimeoutSettingKey is the user setting for the per-call
	// timeout in seconds. Minimum 30s.
	KnowledgeTimeoutSettingKey = "knowledge_timeout_s"
)

// knowledgeCallTimeoutFromSettings resolves the per-call timeout from user
// settings. Falls back to the default when unset or invalid.
func knowledgeCallTimeoutFromSettings(settings map[string]string) time.Duration {
	raw := strings.TrimSpace(settings[KnowledgeTimeoutSettingKey])
	if raw == "" {
		return defaultKnowledgeCallTimeout
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 30 {
		return defaultKnowledgeCallTimeout
	}
	return time.Duration(secs) * time.Second
}

// checkClaudeCLI verifies the Claude CLI is installed and can run
// non-interactively. Returns the path or an error.
func checkClaudeCLI(ctx context.Context) (string, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found in PATH")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := executil.Command(checkCtx, claudePath, "--version")
	setSysProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude CLI not ready (may need authentication): %s", strings.TrimSpace(string(out)))
	}
	return claudePath, nil
}

// callClaudeForKnowledge calls Claude with a system prompt and optional content.
func callClaudeForKnowledge(ctx context.Context, claudePath string, timeout time.Duration, systemPrompt, content string) (string, error) {
	input := systemPrompt
	if content != "" {
		if len(content) > maxKnowledgeTranscriptChars {
			half := maxKnowledgeTranscriptChars / 2
			content = content[:half] + "\n\n[... middle of transcript truncated ...]\n\n" + content[len(content)-half:]
		}
		input = systemPrompt + "\n\n---\n\nSession transcript:\n\n" + content
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := executil.Command(callCtx, claudePath,
		"--print",
		"--model", "sonnet",
		"--no-session-persistence",
		input,
	)
	setSysProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if callCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude CLI timed out after %v (may need authentication)", timeout)
		}
		if detail != "" {
			return "", fmt.Errorf("claude CLI failed: %w: %s", err, detail)
		}
		return "", fmt.Errorf("claude CLI failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeKnowledgeBundle writes the OKF bundle to disk.
func writeKnowledgeBundle(coralDir string, bundle *KnowledgeBundle) error {
	dir := filepath.Join(coralDir, "agent_knowledge", bundle.TeamName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Write index.md
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(bundle.IndexMD), 0644); err != nil {
		return err
	}

	// Write each agent's OKF file
	for name, concept := range bundle.Agents {
		slug := slugify(name)
		path := filepath.Join(dir, slug+".md")
		if err := os.WriteFile(path, []byte(concept.FinalMD), 0644); err != nil {
			return err
		}
	}

	return nil
}

// buildIndexMD generates the OKF index document for the team.
func buildIndexMD(teamName, timestamp string, agents []agentMeta, bundle *KnowledgeBundle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntype: Team Knowledge Bundle\ntitle: %s\n", teamName)
	fmt.Fprintf(&b, "description: Distilled institutional knowledge for team %s\n", teamName)

	var tagParts []string
	for _, a := range agents {
		tagParts = append(tagParts, slugify(a.name))
	}
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tagParts, ", "))
	fmt.Fprintf(&b, "timestamp: %s\n---\n\n", timestamp)

	fmt.Fprintf(&b, "# %s — Team Knowledge\n\n", teamName)
	fmt.Fprintf(&b, "Auto-distilled from agent session histories.\n\n")

	fmt.Fprintf(&b, "## Agents\n\n")
	for _, a := range agents {
		slug := slugify(a.name)
		fmt.Fprintf(&b, "- [%s](./%s.md) — %s\n", a.name, slug, a.role)
	}

	if len(bundle.Links) > 0 {
		fmt.Fprintf(&b, "\n## Collaboration Links\n\n")
		for _, link := range bundle.Links {
			fromSlug := slugify(link.From)
			toSlug := slugify(link.To)
			fmt.Fprintf(&b, "- [%s](./%s.md) → [%s](./%s.md): %s\n",
				link.From, fromSlug, link.To, toSlug, link.Description)
		}
	}

	return b.String()
}

// parseCollabLinks extracts agent-to-agent links from the collaboration analysis.
func parseCollabLinks(collabResult string, agents []agentMeta) []KnowledgeLink {
	var links []KnowledgeLink
	agentNames := make(map[string]bool)
	for _, a := range agents {
		agentNames[strings.ToLower(a.name)] = true
	}

	lines := strings.Split(collabResult, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") {
			continue
		}
		// Look for "Agent → Agent" or "Agent -> Agent" patterns
		for _, sep := range []string{" → ", " -> ", " hands off to ", " signals ", " depends on ", " communicates with "} {
			if idx := strings.Index(line, sep); idx > 0 {
				before := extractAgentName(line[:idx], agentNames)
				after := extractAgentName(line[idx+len(sep):], agentNames)
				if before != "" && after != "" && before != after {
					links = append(links, KnowledgeLink{
						From:        before,
						To:          after,
						Relation:    strings.TrimSpace(sep),
						Description: strings.TrimLeft(line, "-* "),
					})
				}
			}
		}
	}
	return links
}

// extractAgentName finds the first known agent name in a text fragment.
func extractAgentName(text string, known map[string]bool) string {
	text = strings.TrimSpace(text)
	// Try exact match first
	for name := range known {
		if strings.EqualFold(text, name) {
			return text
		}
	}
	// Try finding a known name as a word
	words := strings.Fields(text)
	for _, w := range words {
		clean := strings.Trim(w, ".,;:\"'()[]")
		if known[strings.ToLower(clean)] {
			return clean
		}
	}
	return ""
}

// Slugify converts a name to a URL-safe slug.
func Slugify(name string) string {
	return slugify(name)
}

func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	var clean strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			clean.WriteRune(c)
		}
	}
	return clean.String()
}

func assembleOKFFallback(agentName, role, teamName, timestamp, pass1, collab string) string {
	slug := slugify(agentName)
	return fmt.Sprintf(`---
type: Agent Knowledge
title: %s
description: Institutional knowledge for %s on team %s
tags: [%s, %s, %s]
timestamp: %s
---

# %s

%s

## Collaboration

%s
`, agentName, agentName, teamName, role, teamName, slug, timestamp, agentName, pass1, collab)
}
