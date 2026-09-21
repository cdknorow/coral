package background

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/subagent"
)

// TokenPoller periodically reads Codex (and future non-Claude) agent transcripts
// to extract token usage data and store it via the TokenUsageStore.
type TokenPoller struct {
	sessionStore *store.SessionStore
	usageStore   *store.TokenUsageStore
	// subagentStore is optional; when nil, subagent spend is not tracked.
	subagentStore *store.SubagentStore
	interval      time.Duration
	logger        *slog.Logger

	// Track last-polled mtime per rollout file to skip unchanged files.
	lastMtime map[string]time.Time
	// Cache mapping session ID → rollout file path (resolved once per session).
	sessionPaths map[string]string
	// Track the last cumulative totals per session to compute deltas.
	lastCumulative map[string]cumulativeTokens
	// Track how many token_count entries we've already processed per session.
	lastEntryCount map[string]int
}

// NewTokenPoller creates a new TokenPoller.
func NewTokenPoller(ss *store.SessionStore, us *store.TokenUsageStore, interval time.Duration) *TokenPoller {
	return &TokenPoller{
		sessionStore:   ss,
		usageStore:     us,
		interval:       interval,
		logger:         slog.Default().With("service", "token_poller"),
		lastMtime:      make(map[string]time.Time),
		sessionPaths:   make(map[string]string),
		lastCumulative: make(map[string]cumulativeTokens),
		lastEntryCount: make(map[string]int),
	}
}

// SetSubagentStore enables tracking of the spend of subagents launched by
// Claude sessions. Must be called before Run.
func (p *TokenPoller) SetSubagentStore(st *store.SubagentStore) {
	p.subagentStore = st
}

// Run starts the polling loop.
func (p *TokenPoller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *TokenPoller) pollOnce(ctx context.Context) {
	sessions, err := p.sessionStore.GetAllLiveSessions(ctx)
	if err != nil {
		p.logger.Error("failed to list live sessions", "error", err)
		return
	}

	for _, ls := range sessions {
		if ls.IsSleeping == 1 {
			continue
		}
		switch ls.AgentType {
		case at.Codex:
			p.pollCodexSession(ctx, &ls)
		case at.Claude:
			p.pollClaudeSession(ctx, &ls)
		case at.Pi:
			p.pollPiSession(ctx, &ls)
		}
	}
}

func (p *TokenPoller) pollCodexSession(ctx context.Context, ls *store.LiveSession) {
	// Resolve the rollout file path (cached per session)
	rolloutPath, ok := p.sessionPaths[ls.SessionID]
	if !ok {
		rolloutPath = findCodexRollout(ls)
		if rolloutPath == "" {
			p.logger.Warn("no rollout file found for Codex session",
				"session_id", ls.SessionID[:8],
				"agent_name", ls.AgentName,
				"working_dir", ls.WorkingDir,
				"created_at", ls.CreatedAt)
			return
		}
		p.logger.Info("matched rollout file for Codex session",
			"session_id", ls.SessionID[:8],
			"path", filepath.Base(rolloutPath))
		p.sessionPaths[ls.SessionID] = rolloutPath
	}

	// Check mtime to skip unchanged files
	info, err := os.Stat(rolloutPath)
	if err != nil {
		return
	}
	if lastMtime, ok := p.lastMtime[rolloutPath]; ok && !info.ModTime().After(lastMtime) {
		return
	}
	p.lastMtime[rolloutPath] = info.ModTime()

	// Extract all cumulative token_count entries from the rollout file
	usage := extractCodexUsage(rolloutPath)
	if usage == nil || len(usage.Calls) == 0 {
		return
	}

	// Skip entries we've already processed
	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if len(usage.Calls) <= alreadyProcessed {
		return
	}
	newCalls := usage.Calls[alreadyProcessed:]

	agentName := ""
	if ls.DisplayName != nil {
		agentName = *ls.DisplayName
	}

	// Get the last cumulative totals for delta computation
	prev := p.lastCumulative[ls.SessionID]

	for _, call := range newCalls {
		// Compute delta from previous cumulative totals
		deltaInput := call.InputTokens - prev.Input
		deltaOutput := call.OutputTokens - prev.Output
		deltaCached := call.CachedInput - prev.Cached
		deltaTotal := call.TotalTokens - prev.Total

		// Skip zero-delta entries (e.g. first token_count with all zeros)
		if deltaInput == 0 && deltaOutput == 0 {
			prev = cumulativeTokens{
				Input: call.InputTokens, Output: call.OutputTokens,
				Cached: call.CachedInput, Total: call.TotalTokens,
			}
			continue
		}

		// The fallback exists only to pick a price. The row records call.Model,
		// what was actually observed, so the column never holds a guess.
		model := call.Model
		if model == "" {
			model = "gpt-5.4"
		}
		costUSD := estimateCost(model, deltaInput, deltaOutput, deltaCached, 0)

		record := &store.TokenUsage{
			SessionID:       ls.SessionID,
			AgentName:       agentName,
			AgentType:       at.Codex,
			TeamID:          ls.TeamID,
			BoardName:       ls.BoardName,
			InputTokens:     deltaInput,
			OutputTokens:    deltaOutput,
			CacheReadTokens: deltaCached,
			TotalTokens:     deltaTotal,
			CostUSD:         costUSD,
			SessionStartAt:  usage.SessionStartAt,
			LastActivityAt:  call.Timestamp,
			RecordedAt:      call.Timestamp,
			Source:          "jsonl",
			Model:           call.Model,
		}

		if err := p.usageStore.RecordUsage(ctx, record); err != nil {
			p.logger.Error("failed to record Codex usage", "session_id", ls.SessionID, "error", err)
			continue
		}

		p.logger.Debug("recorded Codex call",
			"session_id", ls.SessionID[:8],
			"model", model,
			"delta_input", deltaInput,
			"delta_output", deltaOutput,
			"cost_usd", costUSD,
			"timestamp", call.Timestamp)

		// Update cumulative tracking
		prev = cumulativeTokens{
			Input: call.InputTokens, Output: call.OutputTokens,
			Cached: call.CachedInput, Total: call.TotalTokens,
		}
	}

	p.lastCumulative[ls.SessionID] = prev
	p.lastEntryCount[ls.SessionID] = len(usage.Calls)
}

// codexUsageData holds token usage extracted from a Codex rollout file.
// cumulativeTokens tracks the running cumulative totals for delta computation.
type cumulativeTokens struct {
	Input  int
	Output int
	Cached int
	Total  int
}

// codexCallData represents a single API call's token delta.
type codexCallData struct {
	InputTokens  int
	OutputTokens int
	CachedInput  int
	TotalTokens  int
	Timestamp    string
	Model        string
}

// codexUsageData holds all per-call deltas extracted from a rollout file.
type codexUsageData struct {
	Calls          []codexCallData
	SessionStartAt string
}

// extractCodexUsage reads a Codex rollout JSONL file and extracts ALL
// cumulative token_count entries. The caller computes deltas using tracked state.
func extractCodexUsage(path string) *codexUsageData {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	result := &codexUsageData{}
	var currentModel string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Type string `json:"type"`
				Info struct {
					TotalTokenUsage struct {
						InputTokens           int `json:"input_tokens"`
						CachedInputTokens     int `json:"cached_input_tokens"`
						OutputTokens          int `json:"output_tokens"`
						ReasoningOutputTokens int `json:"reasoning_output_tokens"`
						TotalTokens           int `json:"total_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
				Model string `json:"model"`
				CWD   string `json:"cwd"`
			} `json:"payload"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Type == "session_meta" && entry.Timestamp != "" {
			result.SessionStartAt = entry.Timestamp
		}

		if entry.Type == "turn_context" && entry.Payload.Model != "" {
			currentModel = entry.Payload.Model
		}

		if entry.Type == "event_msg" && entry.Payload.Type == "token_count" {
			tu := entry.Payload.Info.TotalTokenUsage
			result.Calls = append(result.Calls, codexCallData{
				InputTokens:  tu.InputTokens,
				OutputTokens: tu.OutputTokens + tu.ReasoningOutputTokens,
				CachedInput:  tu.CachedInputTokens,
				TotalTokens:  tu.TotalTokens,
				Timestamp:    entry.Timestamp,
				Model:        currentModel,
			})
		}
	}

	if len(result.Calls) == 0 {
		return nil
	}
	return result
}

// findCodexRollout finds the Codex rollout file for a live session.
// Since Codex rollout UUIDs don't match Coral session IDs, we match by:
// 1. Session ID substring in filename (works when Coral controls the ID)
// 2. Most recently modified rollout file whose cwd matches the session's working dir
// 3. Most recently created rollout file near the session's creation time
func findCodexRollout(ls *store.LiveSession) string {
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	basePath := filepath.Join(codexHome, "sessions")

	// Strategy 1: Try matching session ID in filename
	matches, err := filepath.Glob(filepath.Join(basePath, "*", "*", "*", "rollout-*"+ls.SessionID+"*.jsonl"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}

	// Strategy 2: Find recently modified rollout files and match by working dir
	type rolloutFile struct {
		path  string
		mtime time.Time
	}
	var candidates []rolloutFile

	// Parse session creation time (stored as RFC3339 with microseconds)
	sessionCreated, _ := time.Parse(time.RFC3339Nano, ls.CreatedAt)
	if sessionCreated.IsZero() {
		sessionCreated, _ = time.Parse(time.RFC3339, ls.CreatedAt)
	}
	if sessionCreated.IsZero() {
		sessionCreated, _ = time.Parse("2006-01-02T15:04:05Z", ls.CreatedAt)
	}
	if sessionCreated.IsZero() {
		sessionCreated, _ = time.Parse("2006-01-02 15:04:05", ls.CreatedAt)
	}

	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		// Only consider files modified in the last hour or created near session start
		if time.Since(info.ModTime()) < time.Hour {
			candidates = append(candidates, rolloutFile{path: path, mtime: info.ModTime()})
		} else if !sessionCreated.IsZero() && info.ModTime().After(sessionCreated.Add(-5*time.Minute)) {
			candidates = append(candidates, rolloutFile{path: path, mtime: info.ModTime()})
		}
		return nil
	})

	// Sort by most recently modified
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.After(candidates[j].mtime)
	})

	// Check each candidate's cwd against the session's working dir
	for _, c := range candidates {
		cwd := peekCodexCWD(c.path)
		if cwd != "" && cwd == ls.WorkingDir {
			return c.path
		}
	}

	// Fallback: if we have a session creation time, pick the rollout file
	// created closest to it
	if !sessionCreated.IsZero() && len(candidates) > 0 {
		var bestPath string
		var bestDelta time.Duration = time.Hour * 24
		for _, c := range candidates {
			// Parse timestamp from filename: rollout-YYYY-MM-DDTHH-MM-SS-{uuid}.jsonl
			base := filepath.Base(c.path)
			if !strings.HasPrefix(base, "rollout-") {
				continue
			}
			// Extract timestamp portion
			parts := strings.SplitN(strings.TrimPrefix(base, "rollout-"), "-", 7)
			if len(parts) >= 6 {
				ts := strings.Join(parts[:6], "-")
				// Format: 2026-03-29T00-03-11 → 2006-01-02T15-04-05
				if t, err := time.Parse("2006-01-02T15-04-05", ts); err == nil {
					delta := t.Sub(sessionCreated).Abs()
					if delta < bestDelta {
						bestDelta = delta
						bestPath = c.path
					}
				}
			}
		}
		if bestPath != "" && bestDelta < 5*time.Minute {
			return bestPath
		}
	}

	return ""
}

// peekCodexCWD reads the first few lines of a Codex rollout file to extract
// the working directory from a turn_context entry.
func peekCodexCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Read first 32KB — turn_context is usually in the first few entries
	buf := make([]byte, 32*1024)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}

	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				CWD string `json:"cwd"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		// Both session_meta and turn_context have cwd
		if (entry.Type == "session_meta" || entry.Type == "turn_context") && entry.Payload.CWD != "" {
			return entry.Payload.CWD
		}
	}
	return ""
}

// ── Claude JSONL token tracking ───────────────────────────────

// claudeCallData represents a single assistant turn's token usage from a Claude JSONL file.
type claudeCallData struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Model            string
	Timestamp        string
}

// claudeUsageData holds all per-turn usage records extracted from a Claude JSONL file.
type claudeUsageData struct {
	Calls          []claudeCallData
	SessionStartAt string
}

func (p *TokenPoller) pollClaudeSession(ctx context.Context, ls *store.LiveSession) {
	// Resolve the JSONL file path (cached per session)
	jsonlPath, ok := p.sessionPaths[ls.SessionID]
	if !ok {
		jsonlPath = resolveClaudeTranscriptPath(ls.SessionID, ls.WorkingDir)
		if jsonlPath == "" {
			return
		}
		p.logger.Info("matched JSONL file for Claude session",
			"session_id", ls.SessionID[:8],
			"path", filepath.Base(jsonlPath))
		p.sessionPaths[ls.SessionID] = jsonlPath
	}

	// Subagents write to their own transcripts, and keep doing so while the
	// main agent sits idle waiting on them. This must run before the mtime
	// check below, which returns early when the MAIN transcript is unchanged.
	p.pollClaudeSubagents(ctx, ls, jsonlPath)

	// Check mtime to skip unchanged files
	info, err := os.Stat(jsonlPath)
	if err != nil {
		return
	}
	if lastMtime, ok := p.lastMtime[jsonlPath]; ok && !info.ModTime().After(lastMtime) {
		return
	}
	p.lastMtime[jsonlPath] = info.ModTime()

	usage := extractClaudeUsage(jsonlPath)
	if usage == nil || len(usage.Calls) == 0 {
		return
	}

	// Team launches may not know the concrete Claude model up front. Learn it
	// from transcript metadata so the persisted context denominator self-heals.
	for i := len(usage.Calls) - 1; i >= 0; i-- {
		observedModel := strings.TrimSpace(usage.Calls[i].Model)
		if observedModel == "" {
			continue
		}
		window := proxy.LookupContextWindow(observedModel)
		storedModel := ""
		if ls.Model != nil {
			storedModel = *ls.Model
		}
		if observedModel != storedModel || window != ls.ContextWindow {
			if err := p.sessionStore.UpdateContextWindow(ctx, ls.SessionID, window, observedModel); err != nil {
				p.logger.Error("failed to update Claude model context window", "session_id", ls.SessionID, "model", observedModel, "error", err)
			}
		}
		break
	}

	// Skip entries we've already processed
	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if len(usage.Calls) <= alreadyProcessed {
		return
	}
	newCalls := usage.Calls[alreadyProcessed:]

	agentName := ""
	if ls.DisplayName != nil {
		agentName = *ls.DisplayName
	}

	for _, call := range newCalls {
		// Skip zero-usage entries
		if call.InputTokens == 0 && call.OutputTokens == 0 {
			continue
		}

		// As for Codex: the fallback only picks a price; the row stores call.Model.
		model := call.Model
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
		costUSD := estimateCost(model, call.InputTokens, call.OutputTokens, call.CacheReadTokens, call.CacheWriteTokens)

		record := &store.TokenUsage{
			SessionID:        ls.SessionID,
			AgentName:        agentName,
			AgentType:        at.Claude,
			TeamID:           ls.TeamID,
			BoardName:        ls.BoardName,
			InputTokens:      call.InputTokens,
			OutputTokens:     call.OutputTokens,
			CacheReadTokens:  call.CacheReadTokens,
			CacheWriteTokens: call.CacheWriteTokens,
			TotalTokens:      call.InputTokens + call.OutputTokens + call.CacheReadTokens + call.CacheWriteTokens,
			CostUSD:          costUSD,
			SessionStartAt:   usage.SessionStartAt,
			LastActivityAt:   call.Timestamp,
			RecordedAt:       call.Timestamp,
			Source:           "jsonl",
			Model:            call.Model,
		}

		if err := p.usageStore.RecordUsage(ctx, record); err != nil {
			p.logger.Error("failed to record Claude usage", "session_id", ls.SessionID, "error", err)
			continue
		}

		p.logger.Debug("recorded Claude turn",
			"session_id", ls.SessionID[:8],
			"model", model,
			"input", call.InputTokens,
			"output", call.OutputTokens,
			"cost_usd", costUSD,
			"timestamp", call.Timestamp)
	}

	p.lastEntryCount[ls.SessionID] = len(usage.Calls)
}

// pollClaudeSubagents records the spend of the subagents this session has
// launched. Their API calls are absent from the session's own transcript, so
// without this they would be missing from Coral entirely.
func (p *TokenPoller) pollClaudeSubagents(ctx context.Context, ls *store.LiveSession, parentTranscriptPath string) {
	if p.subagentStore == nil {
		return
	}
	// Only re-read a subagent whose transcript or metadata changed.
	var touched []subagent.Files
	changed := func(f subagent.Files) bool {
		mtime := latestMtime(f.TranscriptPath, f.MetaPath)
		if last, ok := p.lastMtime[f.TranscriptPath]; ok && !mtime.After(last) {
			return false
		}
		touched = append(touched, f)
		return true
	}

	synced, err := syncSubagentSpend(ctx, p.subagentStore, ls.SessionID, parentTranscriptPath, changed)
	if err != nil {
		// Leave lastMtime alone so the failed subagent is retried next poll.
		p.logger.Error("failed to sync subagent spend", "session_id", ls.SessionID, "error", err)
		return
	}
	for _, f := range touched {
		p.lastMtime[f.TranscriptPath] = latestMtime(f.TranscriptPath, f.MetaPath)
	}
	if synced > 0 {
		p.logger.Debug("synced subagent spend", "session_id", ls.SessionID[:8], "subagents", synced)
	}
}

// extractClaudeUsage reads a Claude JSONL file and extracts per-turn token usage
// from assistant entries. Unlike Codex (cumulative), Claude values are per-turn.
func extractClaudeUsage(path string) *claudeUsageData {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	result := &claudeUsageData{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Capture first timestamp as session start
		if entry.Timestamp != "" && result.SessionStartAt == "" {
			result.SessionStartAt = entry.Timestamp
		}

		// Extract usage from assistant entries
		if entry.Type == "assistant" && entry.Message.Usage.InputTokens > 0 {
			result.Calls = append(result.Calls, claudeCallData{
				InputTokens:      entry.Message.Usage.InputTokens,
				OutputTokens:     entry.Message.Usage.OutputTokens,
				CacheReadTokens:  entry.Message.Usage.CacheReadInputTokens,
				CacheWriteTokens: entry.Message.Usage.CacheCreationInputTokens,
				Model:            entry.Message.Model,
				Timestamp:        entry.Timestamp,
			})
		}
	}

	if len(result.Calls) == 0 {
		return nil
	}
	return result
}

// resolveClaudeTranscriptPath finds the Claude JSONL file for a session.
func resolveClaudeTranscriptPath(sessionID, workingDir string) string {
	home, _ := os.UserHomeDir()
	basePath := os.Getenv("CLAUDE_PROJECTS_DIR")
	if basePath == "" {
		basePath = filepath.Join(home, ".claude", "projects")
	}

	// Try working directory hint first
	if workingDir != "" {
		encoded := strings.ReplaceAll(workingDir, "/", "-")
		candidate := filepath.Join(basePath, encoded, sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Search all project dirs
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(basePath, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// ── Pi JSONL token tracking ─────────────────────────────────

type piCallData struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	TotalTokens      int
	CostUSD          float64
	Model            string
	Timestamp        string
}

type piUsageData struct {
	Calls          []piCallData
	SessionStartAt string
}

func (p *TokenPoller) pollPiSession(ctx context.Context, ls *store.LiveSession) {
	jsonlPath, ok := p.sessionPaths[ls.SessionID]
	if !ok {
		jsonlPath = resolvePiSessionPath(ls.SessionID)
		if jsonlPath == "" {
			return
		}
		p.logger.Info("matched JSONL file for Pi session",
			"session_id", ls.SessionID[:8],
			"path", filepath.Base(jsonlPath))
		p.sessionPaths[ls.SessionID] = jsonlPath
	}

	info, err := os.Stat(jsonlPath)
	if err != nil {
		return
	}
	if lastMtime, ok := p.lastMtime[jsonlPath]; ok && !info.ModTime().After(lastMtime) {
		return
	}
	p.lastMtime[jsonlPath] = info.ModTime()

	usage := extractPiUsage(jsonlPath)
	if usage == nil || len(usage.Calls) == 0 {
		return
	}

	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if len(usage.Calls) <= alreadyProcessed {
		return
	}
	newCalls := usage.Calls[alreadyProcessed:]

	agentName := ""
	if ls.DisplayName != nil {
		agentName = *ls.DisplayName
	}

	for _, call := range newCalls {
		if call.InputTokens == 0 && call.OutputTokens == 0 {
			continue
		}

		record := &store.TokenUsage{
			SessionID:        ls.SessionID,
			AgentName:        agentName,
			AgentType:        at.Pi,
			TeamID:           ls.TeamID,
			BoardName:        ls.BoardName,
			InputTokens:      call.InputTokens,
			OutputTokens:     call.OutputTokens,
			CacheReadTokens:  call.CacheReadTokens,
			CacheWriteTokens: call.CacheWriteTokens,
			TotalTokens:      call.TotalTokens,
			CostUSD:          call.CostUSD,
			SessionStartAt:   usage.SessionStartAt,
			LastActivityAt:   call.Timestamp,
			RecordedAt:       call.Timestamp,
			Source:           "jsonl",
			Model:            call.Model,
		}

		if err := p.usageStore.RecordUsage(ctx, record); err != nil {
			p.logger.Error("failed to record Pi usage", "session_id", ls.SessionID, "error", err)
			continue
		}

		p.logger.Debug("recorded Pi turn",
			"session_id", ls.SessionID[:8],
			"model", call.Model,
			"input", call.InputTokens,
			"output", call.OutputTokens,
			"cost_usd", call.CostUSD,
			"timestamp", call.Timestamp)
	}

	p.lastEntryCount[ls.SessionID] = len(usage.Calls)
}

func extractPiUsage(path string) *piUsageData {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	result := &piUsageData{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage struct {
					Input       int `json:"input"`
					Output      int `json:"output"`
					CacheRead   int `json:"cacheRead"`
					CacheWrite  int `json:"cacheWrite"`
					TotalTokens int `json:"totalTokens"`
					Cost        struct {
						Total float64 `json:"total"`
					} `json:"cost"`
				} `json:"usage"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Timestamp != "" && result.SessionStartAt == "" {
			result.SessionStartAt = entry.Timestamp
		}

		if entry.Type == "message" && entry.Message.Role == "assistant" && entry.Message.Usage.Input > 0 {
			result.Calls = append(result.Calls, piCallData{
				InputTokens:      entry.Message.Usage.Input,
				OutputTokens:     entry.Message.Usage.Output,
				CacheReadTokens:  entry.Message.Usage.CacheRead,
				CacheWriteTokens: entry.Message.Usage.CacheWrite,
				TotalTokens:      entry.Message.Usage.TotalTokens,
				CostUSD:          entry.Message.Usage.Cost.Total,
				Model:            entry.Message.Model,
				Timestamp:        entry.Timestamp,
			})
		}
	}

	if len(result.Calls) == 0 {
		return nil
	}
	return result
}

// resolvePiSessionPath finds the Pi JSONL file for a Coral session.
// With --session-dir, Pi stores sessions under ~/.coral/pi-sessions/<session-id>/.
// Falls back to searching Pi's default session directory.
func resolvePiSessionPath(sessionID string) string {
	home, _ := os.UserHomeDir()

	// Strategy 1: Check Coral-managed pi-sessions dir
	coralDir := os.Getenv("CORAL_DATA_DIR")
	if coralDir == "" {
		coralDir = filepath.Join(home, ".coral")
	}
	piSessionDir := filepath.Join(coralDir, "pi-sessions", sessionID)
	if matches, err := filepath.Glob(filepath.Join(piSessionDir, "*.jsonl")); err == nil && len(matches) > 0 {
		return matches[0]
	}

	// Strategy 2: Search Pi's default session directories for a matching session ID
	piBase := os.Getenv("PI_CODING_AGENT_DIR")
	if piBase == "" {
		piBase = filepath.Join(home, ".pi", "agent", "sessions")
	}
	var found string
	filepath.Walk(piBase, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") && strings.Contains(filepath.Base(path), sessionID) {
			found = path
		}
		return nil
	})
	return found
}

// estimateCost calculates estimated cost based on model pricing.
func estimateCost(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	breakdown := proxy.CalculateCostBreakdown(model, proxy.TokenUsage{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	})
	return breakdown.TotalCostUSD
}
