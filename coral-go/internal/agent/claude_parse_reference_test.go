package agent

import (
	"bufio"
	"encoding/json"
	"path/filepath"
	"strings"
)

// The whole-file parse the indexer used before #3728, kept as the reference the incremental
// reader is checked against (claude_history_test.go).
func parseClaudeSessions(fpath string, mtime float64) ([]IndexedSession, error) {
	f, err := openHistoryFile(fpath) // regular file, never a link or FIFO (#3601, W21)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sessionID := strings.TrimSuffix(filepath.Base(fpath), ".jsonl")
	var firstTS, lastTS *string
	var msgCount int
	var summaryParts []string
	var firstMsg string // the first thing the person typed (#6207)
	var fts FTSBodyBuilder

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		ts, _ := entry["timestamp"].(string)
		if ts != "" {
			if firstTS == nil {
				firstTS = &ts
			}
			lastCopy := ts
			lastTS = &lastCopy
		}
		etype, _ := entry["type"].(string)
		if etype == "user" || etype == "assistant" {
			msgCount++
			if msg, _ := entry["message"].(map[string]any); msg != nil {
				text := extractFirstText(msg["content"])
				fts.Add(text)
				if t, ok := typedText(text); etype == "user" && firstMsg == "" && ok {
					firstMsg = clipFirstMessage(t)
				}
			}
		}
		// Grab first assistant text for display summary
		if etype == "assistant" && len(summaryParts) < 1 {
			if msg, _ := entry["message"].(map[string]any); msg != nil {
				if text := extractFirstText(msg["content"]); text != "" {
					if len(text) > 200 {
						text = text[:200]
					}
					summaryParts = append(summaryParts, text)
				}
			}
		}
	}
	if msgCount == 0 {
		return nil, nil
	}
	summary := strings.Join(summaryParts, " ")
	return []IndexedSession{{
		SessionID:        sessionID,
		SourceType:       "claude",
		SourceFile:       fpath,
		FileMtime:        mtime,
		FirstTimestamp:   firstTS,
		LastTimestamp:    lastTS,
		MessageCount:     msgCount,
		DisplaySummary:   summary,
		FirstUserMessage: firstMsg,
		IndexVersion:     HistoryIndexVersion,
		FTSBody:          buildFTSBody(summary, &fts),
	}}, nil
}
