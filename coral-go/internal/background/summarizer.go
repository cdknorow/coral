package background

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/store"
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
		settings, serr := sessionStore.GetSettingsForCLI(ctx)
		var summary string
		if serr != nil { // never run the CLI found on PATH instead of the configured one (#4350)
			err = fmt.Errorf("reading Coral's settings: %w", serr)
		} else {
			summary, err = callClaude(ctx, settings, transcript)
		}
		if agentsignin.IsNotSignedIn(err) {
			return err // left pending: the batch loop waits, and the summary is made once Claude is signed in (#5469)
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

// transcriptCutMarker stands for the middle of a transcript longer than maxTranscriptChars.
const transcriptCutMarker = "\n\n[... middle of conversation truncated ...]\n\n"

// condenseMessages' walk stops, keeping what it has, after maxUncheckedMessages messages that
// couldn't be checked for secrets (each can take the redactor's whole time budget) or after
// condenseWalkBudget in all, so no transcript makes it walk the whole file (#4414).
const maxUncheckedMessages = 20

var condenseWalkBudget = 10 * time.Second

// condenseMessages extracts text from JSONL entries and truncates to maxTranscriptChars, keeping
// the first and last half.
//
// Each message is redacted whole, before the join and the cut, which could otherwise leave a
// fragment of a token too short for the rules to know (#4414); a message that can't be checked is
// left out. Only the messages that reach the kept text are read and redacted: they are taken from
// the front until the head holds half the limit and from the back until the tail does, so the
// middle of a long transcript is never parsed (a message across the cut is redacted whole). The
// result is what redacting every message, joining them and cutting would give.
func condenseMessages(data []byte) string {
	lines := strings.Split(string(data), "\n")
	half := maxTranscriptChars / 2
	var head, tail []string // tail is in reverse order
	headLen, tailLen := 0, 0
	add := func(parts []string, n int, part string) ([]string, int) {
		if len(parts) > 0 {
			n += len("\n\n")
		}
		return append(parts, part), n + len(part)
	}
	deadline, unchecked := time.Now().Add(condenseWalkBudget), 0
	more := func() bool { return unchecked < maxUncheckedMessages && time.Now().Before(deadline) }
	i, j := 0, len(lines)
	for i < j && headLen < half && more() {
		part, ok, failed := condensedMessage(lines[i])
		if ok {
			head, headLen = add(head, headLen, part)
		} else if failed {
			unchecked++
		}
		i++
	}
	for j > i && tailLen < half && more() {
		j--
		part, ok, failed := condensedMessage(lines[j])
		if ok {
			tail, tailLen = add(tail, tailLen, part)
		} else if failed {
			unchecked++
		}
	}
	for l, r := 0, len(tail)-1; l < r; l, r = l+1, r-1 {
		tail[l], tail[r] = tail[r], tail[l]
	}
	headText, tailText := strings.Join(head, "\n\n"), strings.Join(tail, "\n\n")
	if i < j {
		// Lines in the middle were never read: the head and the tail each hold at least half
		// (then the whole text is longer than the limit and the cut keeps only what was read), or
		// the walk stopped early and they hold what it read.
		if len(headText) > half {
			headText = headText[:half]
		}
		if len(tailText) > half {
			tailText = tailText[len(tailText)-half:]
		}
		return headText + transcriptCutMarker + tailText
	}
	fullText := headText
	if headText != "" && tailText != "" {
		fullText += "\n\n"
	}
	fullText += tailText
	if len(fullText) > maxTranscriptChars {
		fullText = fullText[:half] + transcriptCutMarker + fullText[len(fullText)-half:]
	}
	return fullText
}

// condensedMessage is one JSONL line as condenseMessages shows it ("### Role\ntext"), redacted;
// ok is false for a line that isn't a message with text, or can't be checked for secrets
// (unchecked).
func condensedMessage(line string) (part string, ok, unchecked bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false, false
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return "", false, false
	}
	msgType, _ := entry["type"].(string)
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return "", false, false
	}
	content, err := redactKnowledge(extractContent(msg))
	if err != nil {
		return "", false, true
	}
	if content == "" {
		return "", false, false
	}
	role := "Assistant"
	if msgType == "human" || msgType == "user" {
		role = "User"
	}
	return fmt.Sprintf("### %s\n%s", role, content), true, false
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
	if err := agentsignin.Require(ctx, at.Claude, claudePath); err != nil {
		return "", err // not signed in (#5469): the summary is left pending, never saved as a failure
	}

	prompt := summarizerPrompt + "\n\nPlease summarize this coding session:\n\n" + transcript

	// The transcript goes on stdin, not argv, where it would show in the process list (#3519).
	cmd := executil.Command(ctx, claudePath,
		"--print",
		"--model", "haiku",
		"--no-session-persistence",
	)
	cmd.Stdin = strings.NewReader(prompt)
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
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
