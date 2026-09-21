package background

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/subagent"
)

// syncSubagentSpend brings the subagents table up to date for one main agent.
//
// It finds the subagent transcripts beside the main agent's transcript, prices
// each one, and upserts its cumulative totals under sessionID. Every transcript
// is re-read in full and stored as totals, so the operation is idempotent: it
// is safe to run on every poll and after a restart.
//
// changed reports whether a subagent's files need re-reading; pass nil to
// always re-read. One unreadable subagent does not stop the others: the first
// error is returned after all have been attempted. It returns how many
// subagents were written.
func syncSubagentSpend(ctx context.Context, st *store.SubagentStore, sessionID, parentTranscriptPath string, changed func(subagent.Files) bool) (int, error) {
	files, err := subagent.Discover(parentTranscriptPath)
	if err != nil {
		return 0, fmt.Errorf("discover subagents: %w", err)
	}

	synced := 0
	var firstErr error
	for _, f := range files {
		if changed != nil && !changed(f) {
			continue
		}
		if err := syncOneSubagent(ctx, st, sessionID, f); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("subagent %s: %w", f.SubagentID, err)
			}
			continue
		}
		synced++
	}
	return synced, firstErr
}

func syncOneSubagent(ctx context.Context, st *store.SubagentStore, sessionID string, f subagent.Files) error {
	t, err := subagent.ParseTranscript(f.TranscriptPath)
	if err != nil {
		return err
	}
	// Metadata is a nice-to-have; spend is recorded without it.
	meta, _ := subagent.ReadMeta(f.MetaPath)

	totals := t.Totals()
	return st.UpsertSubagent(ctx, &store.Subagent{
		// The row belongs to the main agent whose transcript directory the file
		// was found under, which is what sessionID identifies.
		SessionID:        sessionID,
		SubagentID:       f.SubagentID,
		SubagentType:     strPtrOrNil(meta.AgentType),
		Description:      strPtrOrNil(meta.Description),
		ToolUseID:        strPtrOrNil(meta.ToolUseID),
		Model:            strPtrOrNil(t.PrimaryModel()),
		SpawnDepth:       meta.SpawnDepth,
		APICalls:         totals.APICalls,
		InputTokens:      totals.InputTokens,
		OutputTokens:     totals.OutputTokens,
		CacheReadTokens:  totals.CacheReadTokens,
		CacheWriteTokens: totals.CacheWriteTokens,
		CostUSD:          subagentCostUSD(t),
		StartedAt:        strPtrOrNil(t.StartedAt),
		LastActivityAt:   strPtrOrNil(t.LastActivityAt),
	})
}

// subagentCostUSD prices each call with its own model, since a subagent is
// not guaranteed to use one model throughout. Calls whose model has no known
// pricing contribute nothing rather than a guess.
func subagentCostUSD(t *subagent.Transcript) float64 {
	var total float64
	for _, c := range t.Calls {
		total += proxy.CalculateCost(c.Model, proxy.TokenUsage{
			InputTokens:      c.InputTokens,
			OutputTokens:     c.OutputTokens,
			CacheReadTokens:  c.CacheReadTokens,
			CacheWriteTokens: c.CacheWriteTokens,
		})
	}
	return total
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// latestMtime returns the newest modification time among paths that exist.
func latestMtime(paths ...string) time.Time {
	var latest time.Time
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}
