package background

import (
	"context"
	"github.com/subgentic/coral/internal/logsafe"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/subagent"
	"github.com/subgentic/coral/internal/transcriptlink"
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
	// mtimeOwner maps each lastMtime path to its session, so the state of a
	// session that is no longer live can be dropped (token_poller_prune.go).
	mtimeOwner map[string]string
	// Incremental transcript readers per session (token_usage_cursor.go).
	codexCursors  map[string]*usageCursor[codexParser]
	claudeCursors map[string]*usageCursor[claudeParser]
	piCursors     map[string]*usageCursor[piParser]
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
		mtimeOwner:     make(map[string]string),
		codexCursors:   make(map[string]*usageCursor[codexParser]),
		claudeCursors:  make(map[string]*usageCursor[claudeParser]),
		piCursors:      make(map[string]*usageCursor[piParser]),
	}
}

// SetSubagentStore enables tracking of the spend of subagents launched by
// Claude sessions. Must be called before Run.
func (p *TokenPoller) SetSubagentStore(st *store.SubagentStore) {
	p.subagentStore = st
}

// Run starts the polling loop.
func (p *TokenPoller) Run(ctx context.Context) error {
	return runOnTicker(ctx, p.interval, func() { p.pollOnce(ctx) })
}

func (p *TokenPoller) pollOnce(ctx context.Context) {
	sessions, err := p.sessionStore.GetAllLiveSessions(ctx)
	if err != nil {
		p.logger.Error("failed to list live sessions", "error", err)
		return
	}

	defer p.pruneEnded(sessions)
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
				"working_dir", logsafe.Dir(ls.WorkingDir),
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
	if lastMtime, ok := p.lastMtime[rolloutPath]; ok && !info.ModTime().After(lastMtime) && !cursorPending(p.codexCursors, ls.SessionID) {
		return
	}
	p.setMtime(ls.SessionID, rolloutPath, info.ModTime())

	// Parse the token_count entries appended since the last poll.
	cur := cursorFor(p.codexCursors, ls.SessionID)
	appended, first := scanUsage(cur, rolloutPath, (*codexParser).line, (*codexParser).takeCalls)
	total := cur.total
	if total == 0 {
		return
	}

	// Skip entries we've already processed
	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if total <= alreadyProcessed {
		return
	}
	newCalls := unprocessed(appended, first, alreadyProcessed)
	sessionStartAt := cur.parser.SessionStartAt

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
				Cached: call.CachedInput, Total: call.TotalTokens, Reasoning: call.Reasoning,
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
			SessionStartAt:  sessionStartAt,
			LastActivityAt:  call.Timestamp,
			RecordedAt:      call.Timestamp,
			Source:          "jsonl",
			Model:           call.Model,
			Thinking:        call.Effort,
			ThinkingTokens:  call.Reasoning - prev.Reasoning,
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
			Cached: call.CachedInput, Total: call.TotalTokens, Reasoning: call.Reasoning,
		}
	}

	p.lastCumulative[ls.SessionID] = prev
	p.lastEntryCount[ls.SessionID] = total
}

// codexUsageData holds token usage extracted from a Codex rollout file.
// cumulativeTokens tracks the running cumulative totals for delta computation.
type cumulativeTokens struct {
	Input     int
	Output    int
	Cached    int
	Total     int
	Reasoning int
}

// codexCallData represents a single API call's token delta.
type codexCallData struct {
	InputTokens  int
	OutputTokens int
	CachedInput  int
	TotalTokens  int
	Timestamp    string
	Model        string
	Effort       string // the reasoning level of the turn this call belongs to ("" when it recorded none)
	Reasoning    int    // the cumulative reasoning_output_tokens
}

// codexUsageData holds all per-call deltas extracted from a rollout file.
type codexUsageData struct {
	Calls          []codexCallData
	SessionStartAt string
}

// extractCodexUsage reads a Codex rollout JSONL file and extracts ALL
// cumulative token_count entries. The caller computes deltas using tracked state.
func extractCodexUsage(path string) *codexUsageData {
	f, err := transcriptlink.OpenRollout(path) // never through a symlink (#3599)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil
	}
	var p codexParser
	for _, line := range strings.Split(string(data), "\n") {
		p.line(line)
	}
	if len(p.Calls) == 0 {
		return nil
	}
	return &p.codexUsageData
}

// findCodexRollout finds the Codex rollout file for a live session, by what names the session
// (its link, its id in the file name, its Coral marker): never by working directory or time
// (#4590). Another Codex session in the same folder and CODEX_HOME would be picked up as this
// one's, and its tokens and cost counted here.
func findCodexRollout(ls *store.LiveSession) string {
	return jsonl.CodexTranscriptPath(ls.SessionID)
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
	Effort           string // the thinking level the turn ran at, from the transcript ("" when it recorded none)
	ThinkingTokens   int    // the thinking tokens the CLI reported for the call
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
	if lastMtime, ok := p.lastMtime[jsonlPath]; ok && !info.ModTime().After(lastMtime) && !cursorPending(p.claudeCursors, ls.SessionID) {
		return
	}
	p.setMtime(ls.SessionID, jsonlPath, info.ModTime())

	cur := cursorFor(p.claudeCursors, ls.SessionID)
	appended, first := scanUsage(cur, jsonlPath, (*claudeParser).line, (*claudeParser).takeCalls)
	total := cur.total
	if total == 0 {
		return
	}

	// Team launches may not know the concrete Claude model up front. Learn it
	// from transcript metadata so the persisted context denominator self-heals.
	if observedModel := cur.parser.lastModel; observedModel != "" {
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
	}

	// Skip entries we've already processed
	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if total <= alreadyProcessed {
		return
	}
	newCalls := unprocessed(appended, first, alreadyProcessed)
	sessionStartAt := cur.parser.SessionStartAt

	agentName := ""
	if ls.DisplayName != nil {
		agentName = *ls.DisplayName
	}

	var recs []*store.TokenUsage // this poll's calls, recorded together after the loop (#5074)
	var priced []string          // the model each was priced as, for the debug line
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
			SessionStartAt:   sessionStartAt,
			LastActivityAt:   call.Timestamp,
			RecordedAt:       call.Timestamp,
			Source:           "jsonl",
			Model:            call.Model,
			Thinking:         call.Effort,
			ThinkingTokens:   call.ThinkingTokens,
		}

		recs = append(recs, record)
		priced = append(priced, model)
	}
	for i, err := range p.usageStore.RecordUsageBatch(ctx, recs) {
		if err != nil {
			p.logger.Error("failed to record Claude usage", "session_id", ls.SessionID, "error", err)
			continue
		}
		p.logger.Debug("recorded Claude turn",
			"session_id", ls.SessionID[:8],
			"model", priced[i],
			"input", recs[i].InputTokens,
			"output", recs[i].OutputTokens,
			"cost_usd", recs[i].CostUSD,
			"timestamp", recs[i].RecordedAt)
	}

	p.lastEntryCount[ls.SessionID] = total
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
		p.setMtime(ls.SessionID, f.TranscriptPath, latestMtime(f.TranscriptPath, f.MetaPath))
	}
	if synced > 0 {
		p.logger.Debug("synced subagent spend", "session_id", ls.SessionID[:8], "subagents", synced)
	}
}

// extractClaudeUsage reads a Claude JSONL file and extracts per-turn token usage
// from assistant entries. Unlike Codex (cumulative), Claude values are per-turn.
func extractClaudeUsage(path string) *claudeUsageData {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var p claudeParser
	for _, line := range strings.Split(string(data), "\n") {
		p.line(line)
	}
	if len(p.Calls) == 0 {
		return nil
	}
	return &p.claudeUsageData
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
		encoded := transcriptlink.ClaudeProjectDirName(workingDir)
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
	if lastMtime, ok := p.lastMtime[jsonlPath]; ok && !info.ModTime().After(lastMtime) && !cursorPending(p.piCursors, ls.SessionID) {
		return
	}
	p.setMtime(ls.SessionID, jsonlPath, info.ModTime())

	cur := cursorFor(p.piCursors, ls.SessionID)
	appended, first := scanUsage(cur, jsonlPath, (*piParser).line, (*piParser).takeCalls)
	total := cur.total
	if total == 0 {
		return
	}

	alreadyProcessed := p.lastEntryCount[ls.SessionID]
	if total <= alreadyProcessed {
		return
	}
	newCalls := unprocessed(appended, first, alreadyProcessed)
	sessionStartAt := cur.parser.SessionStartAt

	agentName := ""
	if ls.DisplayName != nil {
		agentName = *ls.DisplayName
	}

	var recs []*store.TokenUsage // this poll's calls, recorded together after the loop (#5074)
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
			SessionStartAt:   sessionStartAt,
			LastActivityAt:   call.Timestamp,
			RecordedAt:       call.Timestamp,
			Source:           "jsonl",
			Model:            call.Model,
		}

		recs = append(recs, record)
	}
	for i, err := range p.usageStore.RecordUsageBatch(ctx, recs) {
		if err != nil {
			p.logger.Error("failed to record Pi usage", "session_id", ls.SessionID, "error", err)
			continue
		}
		p.logger.Debug("recorded Pi turn",
			"session_id", ls.SessionID[:8],
			"model", recs[i].Model,
			"input", recs[i].InputTokens,
			"output", recs[i].OutputTokens,
			"cost_usd", recs[i].CostUSD,
			"timestamp", recs[i].RecordedAt)
	}

	p.lastEntryCount[ls.SessionID] = total
}

func extractPiUsage(path string) *piUsageData {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var p piParser
	for _, line := range strings.Split(string(data), "\n") {
		p.line(line)
	}
	if len(p.Calls) == 0 {
		return nil
	}
	return &p.piUsageData
}

// resolvePiSessionPath finds the Pi JSONL file for a Coral session.
// With --session-dir, Pi stores sessions under ~/.coral/pi-sessions/<session-id>/.
// Falls back to searching Pi's default session directory.
func resolvePiSessionPath(sessionID string) string {
	if !transcriptlink.IsSessionID(sessionID) { // joined into a path and matched in file names
		return ""
	}
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
