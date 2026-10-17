package background

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/store"
)

const summarizerPrompt = `You are a session summarizer. You will be given a chat transcript or log of chats between a user and an AI coding assistant, produce a concise markdown summary with:

1. A one-line **title** (## heading)
2. A brief **summary** paragraph (2-3 sentences describing what was accomplished)
3. A **task checklist** of what was done (using - [x] for completed, - [ ] for incomplete)
4. **Key files** modified (bulleted list, if identifiable)

Keep it concise — under 300 words total. Use markdown formatting. Do not ask for more information, do the best with what you have been given.`

const maxTranscriptChars = 30000

// BuildSummarizeFn creates a summarize function for use with BatchSummarizer.
func BuildSummarizeFn(sessionStore *store.SessionStore) func(ctx context.Context, sessionID string) error {
	return func(ctx context.Context, sessionID string) error {
		// Check if user has edited notes — don't overwrite
		notes, err := sessionStore.GetSessionNotes(ctx, sessionID)
		if err == nil && notes != nil && notes.IsUserEdited {
			return nil
		}

		// Find the JSONL source file from session_index
		transcript, err := loadTranscript(ctx, sessionStore, sessionID)
		if err != nil || transcript == "" {
			return fmt.Errorf("no transcript for session %s: %v", sessionID, err)
		}

		// Call Claude CLI to generate summary
		settings, serr := sessionStore.GetSettings(context.WithoutCancel(ctx))
		var summary string
		if serr != nil { // never run the CLI found on PATH instead of the configured one (#4350)
			err = fmt.Errorf("reading Coral's settings: %w", serr)
		} else {
			summary, err = callClaude(ctx, settings, transcript)
		}
		if err != nil {
			summary = fmt.Sprintf("*Auto-summarization failed: %v*", err)
		}

		// Save the auto-summary
		return sessionStore.SaveAutoSummary(ctx, sessionID, summary)
	}
}

// loadTranscript reads and condenses JSONL messages for a session.
func loadTranscript(ctx context.Context, ss *store.SessionStore, sessionID string) (string, error) {
	// Get source file from session_index
	var sourceFile string
	err := ss.DB().GetContext(ctx, &sourceFile,
		"SELECT source_file FROM session_index WHERE session_id = ?", sessionID)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(sourceFile)
	if err != nil {
		return "", err
	}

	return condenseMessages(data), nil
}

// condenseMessages extracts text from JSONL entries and truncates to maxTranscriptChars.
func condenseMessages(data []byte) string {
	var parts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		msgType, _ := entry["type"].(string)
		msg, _ := entry["message"].(map[string]any)
		if msg == nil {
			continue
		}

		content := extractContent(msg)
		if content == "" {
			continue
		}

		role := "Assistant"
		if msgType == "human" || msgType == "user" {
			role = "User"
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", role, content))
	}

	fullText := strings.Join(parts, "\n\n")

	// Truncate keeping beginning and end
	if len(fullText) > maxTranscriptChars {
		half := maxTranscriptChars / 2
		fullText = fullText[:half] + "\n\n[... middle of conversation truncated ...]\n\n" + fullText[len(fullText)-half:]
	}

	return fullText
}

// extractContent pulls text from a message content field (string or array of blocks).
func extractContent(msg map[string]any) string {
	switch c := msg["content"].(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var texts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "text" {
				if t, ok := b["text"].(string); ok && t != "" {
					texts = append(texts, t)
				}
			}
		}
		return strings.TrimSpace(strings.Join(texts, "\n"))
	}
	return ""
}

// callClaude calls the Claude CLI to generate a summary.
func callClaude(ctx context.Context, settings map[string]string, transcript string) (string, error) {
	claudePath, err := agent.FindCLI(settings, at.Claude)
	if err != nil {
		return fallbackSummary(transcript), nil
	}

	prompt := summarizerPrompt + "\n\nPlease summarize this coding session:\n\n" + transcript

	// The transcript goes on stdin, not argv, where it would show in the process list (#3519).
	cmd := executil.Command(ctx, claudePath,
		"--print",
		"--model", "haiku",
		"--no-session-persistence",
	)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude CLI failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// fallbackSummary generates a basic extractive summary when Claude CLI is not available.
func fallbackSummary(transcript string) string {
	lines := strings.Split(transcript, "\n")
	var userMsgs []string
	for i, line := range lines {
		if strings.TrimSpace(line) == "### User" {
			var msgLines []string
			for j := i + 1; j < len(lines) && j < i+4; j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "### ") {
					break
				}
				msgLines = append(msgLines, lines[j])
			}
			text := strings.TrimSpace(strings.Join(msgLines, " "))
			if text != "" {
				if len(text) > 100 {
					text = text[:100]
				}
				userMsgs = append(userMsgs, text)
			}
		}
	}

	if len(userMsgs) == 0 {
		return "*No summary available — install Claude Code for AI-powered summaries.*"
	}

	summary := "## Session Summary\n\n**User requests:**\n"
	limit := len(userMsgs)
	if limit > 10 {
		limit = 10
	}
	for i := 0; i < limit; i++ {
		summary += "- " + userMsgs[i] + "\n"
	}
	summary += "\n*Install Claude Code for AI-powered summaries.*"
	return summary
}
