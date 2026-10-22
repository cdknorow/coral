package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/internal/secrets"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/redact"
)

// KnowledgeBundle holds the OKF output for a team.
type KnowledgeBundle struct {
	TeamName  string                   `json:"team_name"`
	CreatedAt string                   `json:"created_at"`
	Agents    map[string]*AgentConcept `json:"agents"`
	Links     []KnowledgeLink          `json:"links"`
	IndexMD   string                   `json:"index_md"`
	// FailedAgents are the agents whose knowledge could not be extracted in this run: their
	// earlier knowledge file, if any, is kept as it was. CollaborationError is set when the
	// cross-referencing pass failed, so the documents were built without it.
	FailedAgents       []DistillFailure `json:"failed_agents,omitempty"`
	CollaborationError string           `json:"collaboration_error,omitempty"`
}

// DistillFailure is an agent a distillation run could not distill, and why.
type DistillFailure struct {
	Agent string `json:"agent"`
	Error string `json:"error"`
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
	if !ValidKnowledgeTeam(teamName) {
		return nil, ErrBadKnowledgeTeam
	}
	settings, serr := ss.GetSettingsForCLI(ctx)
	if serr != nil { // never run the CLI found on PATH instead of the configured one (#4350)
		return nil, fmt.Errorf("reading Coral's settings: %w", serr)
	}
	claudePath, err := checkClaudeCLI(ctx, settings)
	if err != nil {
		return nil, err
	}

	callTimeout := knowledgeCallTimeoutFromSettings(settings)

	bundle := &KnowledgeBundle{
		TeamName:  teamName,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Agents:    make(map[string]*AgentConcept),
	}

	agents, err := teamKnowledgeAgents(ctx, ss, ts, teamName)
	if err != nil {
		return nil, err
	}
	call := func(prompt, content string) (string, error) {
		return callClaudeForKnowledge(ctx, claudePath, callTimeout, prompt, content)
	}
	return distillAgents(bundle, agents, call, coralDir, progressFn)
}

// teamKnowledgeAgents lists the agents a team's distillation covers (#4074): every agent on the
// team's board now, awake or asleep, including ones launched onto the board after the team was
// (they have no roster row), and, for a stopped team, the roster members that stopped with it.
// A member stopped on its own has left the team and is not distilled.
func teamKnowledgeAgents(ctx context.Context, ss *store.SessionStore, ts *store.TeamStore, teamName string) ([]agentMeta, error) {
	live, err := ss.GetBoardSessions(ctx, teamName)
	if err != nil {
		return nil, err
	}
	var team *store.Team
	if ts != nil {
		team, _ = ts.GetTeam(ctx, teamName)
	}

	var agents []agentMeta
	counted := map[string]bool{}
	names := map[string]int{}
	add := func(sessionID, name, prompt string) {
		counted[sessionID] = true
		// Two agents with one name would share one document: the second gets a suffix.
		names[name]++
		if n := names[name]; n > 1 {
			name = fmt.Sprintf("%s (%d)", name, n)
		}
		agents = append(agents, agentMeta{name: name, role: inferRole(prompt), transcript: redactedTranscript(agentTranscript(ctx, ss, sessionID))})
	}
	for _, ls := range live {
		name := ls.AgentName
		if ls.DisplayName != nil && *ls.DisplayName != "" {
			name = *ls.DisplayName
		}
		prompt := ""
		if ls.Prompt != nil {
			prompt = *ls.Prompt
		}
		add(ls.SessionID, name, prompt)
	}
	if team != nil && team.Status == "stopped" && team.StoppedAt != nil {
		for _, m := range team.Members {
			if m.SessionID == nil || counted[*m.SessionID] || m.Status != "stopped" ||
				m.StoppedAt == nil || *m.StoppedAt != *team.StoppedAt {
				continue
			}
			var cfg struct {
				Prompt string `json:"prompt"`
			}
			json.Unmarshal([]byte(m.AgentConfigJSON), &cfg)
			add(*m.SessionID, m.AgentName, cfg.Prompt)
		}
	}

	if len(agents) == 0 {
		return nil, fmt.Errorf("team %q not found", teamName)
	}
	return agents, nil
}

// agentTranscript is a session's transcript, or, while a restarted or resumed session has none
// yet, the newest one along its resume chain.
func agentTranscript(ctx context.Context, ss *store.SessionStore, sessionID string) string {
	if t, err := loadTranscript(ctx, ss, sessionID); err == nil && t != "" {
		return t
	}
	lineage, _ := ss.GetSessionAncestry(ctx, sessionID)
	for _, item := range lineage {
		if item.SessionID == sessionID {
			continue
		}
		if t, err := loadTranscript(ctx, ss, item.SessionID); err == nil && t != "" {
			return t
		}
	}
	return ""
}

// knowledgeRedactor is the redactor agent knowledge goes through (#4414), replaceable in tests.
var knowledgeRedactor = func() redact.Redactor { return secrets.Redactor() }

// redactKnowledge is s with every secret Coral's rules find replaced. Knowledge comes from agent
// transcripts and board text, is kept for good, shown on the dashboard and put into later agents'
// prompts, so it is redacted where it enters (the transcript sent for distilling), where it is
// written and where it is read (files written before this). An error means it couldn't be checked:
// the caller then keeps nothing.
func redactKnowledge(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	out, err := knowledgeRedactor().Text([]byte(s))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// redactedTranscript is a transcript ready to send for distilling: redacted, or "" (as if there
// were none) when it couldn't be checked.
func redactedTranscript(t string) string {
	out, err := redactKnowledge(t)
	if err != nil {
		return ""
	}
	return out
}

// distillAgents runs the three passes over the agents with call (the Claude CLI) and writes
// the bundle. An agent whose extraction fails is reported in FailedAgents and keeps its
// earlier knowledge; when every extraction fails, the run fails and nothing is written.
func distillAgents(bundle *KnowledgeBundle, agents []agentMeta, call func(prompt, content string) (string, error), coralDir string, progressFn func(DistillProgress)) (*KnowledgeBundle, error) {
	teamName := bundle.TeamName
	total := len(agents)

	// ── Pass 1: Individual agent knowledge extraction ──
	failed := map[string]bool{}
	attempted := 0
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
			attempted++
			prompt := fmt.Sprintf(pass1Prompt, a.name, a.role)
			result, err := call(prompt, a.transcript)
			if err != nil {
				// Not the error text: this goes into the other agents' documents.
				concept.Pass1Raw = "Knowledge extraction failed for this agent in this run."
				failed[a.name] = true
				bundle.FailedAgents = append(bundle.FailedAgents, DistillFailure{Agent: a.name, Error: err.Error()})
			} else {
				concept.Pass1Raw = result
			}
		}

		bundle.Agents[a.name] = concept
	}
	if attempted > 0 && len(bundle.FailedAgents) == attempted {
		// Nothing new to write: keep the team's earlier knowledge as a whole.
		return nil, fmt.Errorf("no agent's knowledge could be extracted (%d of %d failed): %s",
			attempted, attempted, bundle.FailedAgents[0].Error)
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
	collabResult, err := call(collabPrompt, "")
	if err != nil {
		bundle.CollaborationError = err.Error()
		collabResult = "No collaboration analysis is available for this run."
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
		if failed[a.name] {
			// Its earlier file (if any) stays; nothing new was learned about it.
			delete(bundle.Agents, a.name)
			continue
		}

		concept := bundle.Agents[a.name]
		slug := slugify(a.name)
		tags := fmt.Sprintf("%s, %s, %s", a.role, teamName, slug)

		prompt := fmt.Sprintf(pass3Prompt,
			a.name, a.role, teamName,
			a.name, a.name, teamName,
			tags, now,
			concept.Pass1Raw, collabResult)

		result, err := call(prompt, "")
		if err != nil {
			// Fallback: assemble OKF manually from pass1+pass2
			concept.FinalMD = assembleOKFFallback(a.name, a.role, teamName, now, concept.Pass1Raw, collabResult)
		} else {
			concept.FinalMD = result
		}
	}

	// Build index.md: a failed agent is listed only if it has earlier knowledge to link to.
	indexed := make([]agentMeta, 0, len(agents))
	for _, a := range agents {
		if !failed[a.name] || LoadAgentKnowledge(coralDir, teamName, a.name) != "" {
			indexed = append(indexed, a)
		}
	}
	bundle.IndexMD = buildIndexMD(teamName, now, indexed, bundle)

	// Write to disk
	if err := writeKnowledgeBundle(coralDir, bundle); err != nil {
		return bundle, fmt.Errorf("knowledge distilled but failed to write: %w", err)
	}

	if progressFn != nil {
		progressFn(DistillProgress{Phase: "complete", Total: total, Completed: total})
	}

	return bundle, nil
}

// ErrBadKnowledgeTeam: a team name that can't name a knowledge folder (#3518).
var ErrBadKnowledgeTeam = errors.New("invalid team name for knowledge")

// ValidKnowledgeTeam reports whether name can name a team's knowledge folder: one plain path
// component, so every read and write stays inside <coral>/agent_knowledge (#3518); no ':'
// either (a Windows alternate data stream).
func ValidKnowledgeTeam(name string) bool {
	return name != "" && len(name) <= 128 && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\:\x00") && filepath.Base(name) == name &&
		safefs.CheckName(name) == nil // a Windows device name, or a trailing dot or space (#3671)
}

// knowledgeTeamDir is a team's knowledge folder, or ErrBadKnowledgeTeam.
func knowledgeTeamDir(coralDir, teamName string) (string, error) {
	if !ValidKnowledgeTeam(teamName) {
		return "", ErrBadKnowledgeTeam
	}
	return filepath.Join(coralDir, "agent_knowledge", teamName), nil
}

// ReadKnowledgeBundle reads an existing knowledge bundle from disk.
func ReadKnowledgeBundle(coralDir, teamName string) (*KnowledgeBundle, error) {
	dir, err := knowledgeTeamDir(coralDir, teamName)
	if err != nil {
		return nil, err
	}
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
	// Redacted on the way out too, for files written before knowledge was redacted (#4414).
	if bundle.IndexMD, err = redactKnowledge(string(indexData)); err != nil {
		return nil, fmt.Errorf("knowledge could not be checked for secrets: %w", err)
	}

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
		md, err := redactKnowledge(string(data))
		if err != nil {
			continue // unchecked knowledge is not shown
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		bundle.Agents[name] = &AgentConcept{
			AgentName: name,
			FinalMD:   md,
		}
	}

	return bundle, nil
}

// LoadAgentKnowledge reads the OKF knowledge file for a specific agent on a team.
func LoadAgentKnowledge(coralDir, teamName, agentName string) string {
	slug := slugify(agentName)
	dir, err := knowledgeTeamDir(coralDir, teamName)
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, slug+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Redacted on the way out too (files written before #4414); unchecked knowledge is none.
	md, err := redactKnowledge(string(data))
	if err != nil {
		return ""
	}
	return md
}

// WriteKnowledgeFile writes a single agent's knowledge file to disk, redacted; redacted says
// whether a secret was replaced.
func WriteKnowledgeFile(coralDir, teamName, agentName, content string) (redacted bool, err error) {
	dir, err := knowledgeTeamDir(coralDir, teamName)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	slug := slugify(agentName)
	md, err := redactKnowledge(content) // #4414: never written unredacted
	if err != nil {
		return false, fmt.Errorf("knowledge not written: it could not be checked for secrets: %w", err)
	}
	return md != content, secfile.WriteFileNoFollow(filepath.Join(dir, slug+".md"), []byte(md), 0o600)
}

// WriteKnowledgeIndex writes the team's index.md knowledge file.
func WriteKnowledgeIndex(coralDir, teamName, content string) error {
	dir, err := knowledgeTeamDir(coralDir, teamName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	md, err := redactKnowledge(content) // #4414: never written unredacted
	if err != nil {
		return fmt.Errorf("knowledge not written: it could not be checked for secrets: %w", err)
	}
	return secfile.WriteFileNoFollow(filepath.Join(dir, "index.md"), []byte(md), 0o600)
}

const (
	// KnowledgeTimeoutSettingKey is the user setting for the per-call
	// timeout in seconds. Minimum 30s.
	KnowledgeTimeoutSettingKey = setreg.KnowledgeTimeoutS
)

// knowledgeCallTimeoutFromSettings resolves the per-call timeout from user
// settings. Falls back to the default when unset or invalid.
func knowledgeCallTimeoutFromSettings(settings map[string]string) time.Duration {
	return time.Duration(setreg.Int(settings, KnowledgeTimeoutSettingKey)) * time.Second
}

// checkClaudeCLI verifies the Claude CLI is installed and can run
// non-interactively. Returns the path or an error.
func checkClaudeCLI(ctx context.Context, settings map[string]string) (string, error) {
	claudePath, err := agent.FindCLI(settings, at.Claude)
	if err != nil {
		return "", err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := executil.Command(checkCtx, claudePath, "--version")
	cmd.Env = agent.CLIEnv(os.Environ(), claudePath) // an npm-installed CLI runs with its own node (#5899)
	setSysProcAttr(cmd)
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude CLI not ready (may need authentication): %s", strings.TrimSpace(string(out)))
	}
	// Signed out: the run ends at once, before any call (#5469). Fresh, so a person who has just signed in is not refused on an old answer.
	if err := agentsignin.RequireFresh(ctx, at.Claude, claudePath); err != nil {
		return "", err
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

	if err := agentsignin.Require(ctx, at.Claude, claudePath); err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The prompt and the transcript go on stdin, never in argv, where any local user
	// could read them in the process list while the call runs (#3519).
	cmd := executil.Command(callCtx, claudePath,
		"--print",
		"--model", "sonnet",
		"--no-session-persistence",
	)
	cmd.Env = agent.CLIEnv(os.Environ(), claudePath) // an npm-installed CLI runs with its own node (#5899)
	cmd.Stdin = strings.NewReader(input)
	setSysProcAttr(cmd)
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
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
	dir, err := knowledgeTeamDir(coralDir, bundle.TeamName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	// Everything is redacted before anything is written: a document that can't be checked stops
	// the write (#4414).
	index, err := redactKnowledge(bundle.IndexMD)
	if err != nil {
		return fmt.Errorf("knowledge not written: it could not be checked for secrets: %w", err)
	}
	finals := make(map[string]string, len(bundle.Agents))
	for name, concept := range bundle.Agents {
		md, err := redactKnowledge(concept.FinalMD)
		if err != nil {
			return fmt.Errorf("knowledge not written: it could not be checked for secrets: %w", err)
		}
		finals[name], concept.FinalMD = md, md
	}
	bundle.IndexMD = index

	// Write index.md
	if err := secfile.WriteFileNoFollow(filepath.Join(dir, "index.md"), []byte(index), 0o600); err != nil {
		return err
	}

	// Write each agent's OKF file
	for name, md := range finals {
		slug := slugify(name)
		path := filepath.Join(dir, slug+".md")
		if err := secfile.WriteFileNoFollow(path, []byte(md), 0o600); err != nil {
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
	// A slug names a file (<slug>.md): one that is a Windows device name ("con", "nul", "com1")
	// gets a '_', the same for every read and write (#3671).
	slug := clean.String()
	if slug != "" && safefs.CheckName(slug) != nil {
		slug += "_"
	}
	return slug
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
