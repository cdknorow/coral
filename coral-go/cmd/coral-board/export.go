package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// exportEntry is a unified message for export, sortable by timestamp.
type exportEntry struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Source    string `json:"source"` // "board" or "side-chat"
}

// exportData is the canonical JSON structure for a board export.
type exportData struct {
	Project     string             `json:"project"`
	ExportedAt  string             `json:"exported_at"`
	Subscribers []exportSubscriber `json:"subscribers"`
	Messages    []exportEntry      `json:"messages"`
	Stats       exportStats        `json:"stats"`
}

type exportSubscriber struct {
	SubscriberID string `json:"subscriber_id"`
	Role         string `json:"role"`
}

type exportStats struct {
	Total    int `json:"total"`
	Board    int `json:"board"`
	SideChat int `json:"side_chat"`
}

// parseMergeFile reads a side-chat markdown file with entries in the format:
//
//	[2026-03-25 01:22] Speaker: message content
//	that can span multiple lines
//
//	[2026-03-25 01:23] Other Speaker: next message
func parseMergeFile(path string) ([]exportEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var entries []exportEntry
	var current *exportEntry

	for _, line := range lines {
		// Check if this line starts a new entry: [YYYY-MM-DD HH:MM] Role: ...
		if len(line) > 18 && line[0] == '[' {
			closeBracket := strings.Index(line, "]")
			if closeBracket > 0 && closeBracket < 25 {
				ts := line[1:closeBracket]
				rest := strings.TrimSpace(line[closeBracket+1:])
				colonIdx := strings.Index(rest, ":")
				if colonIdx > 0 {
					// Save previous entry
					if current != nil {
						current.Content = strings.TrimSpace(current.Content)
						entries = append(entries, *current)
					}
					role := strings.TrimSpace(rest[:colonIdx])
					content := strings.TrimSpace(rest[colonIdx+1:])
					// Normalize timestamp to match board format (YYYY-MM-DDTHH:MM)
					normTS := strings.ReplaceAll(ts, " ", "T")
					current = &exportEntry{
						Timestamp: normTS,
						Role:      role,
						Content:   content,
						Source:    "side-chat",
					}
					continue
				}
			}
		}
		// Continuation line
		if current != nil {
			current.Content += "\n" + line
		}
	}
	// Save last entry
	if current != nil {
		current.Content = strings.TrimSpace(current.Content)
		entries = append(entries, *current)
	}
	return entries, nil
}

// exportPageSize is the most messages the server answers with at a time (it caps a list at 500).
const exportPageSize = 500

// exportMaxMessages bounds an export: past it the export says it stopped, never silently.
const exportMaxMessages = 200_000

// fetchAllMessages reads every message of a board, oldest first, a page at a time by id (after_id). The server caps a page
// at 500, so one request for "limit=10000" used to stop at 500 without saying so (#4628). A server that does not page by
// after_id answers the same newest page again: the export then says so and keeps that page.
func fetchAllMessages(project string, get func(path string) ([]byte, error)) ([]map[string]any, error) {
	var all []map[string]any
	after := int64(0)
	for len(all) < exportMaxMessages {
		data, err := get(fmt.Sprintf("/%s/messages/all?after_id=%d&limit=%d", url.PathEscape(project), after, exportPageSize))
		if err != nil {
			return nil, err
		}
		var page []map[string]any
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("the message list is not a list: %v", err)
		}
		if len(page) == 0 {
			return all, nil
		}
		last, _ := page[len(page)-1]["id"].(float64)
		if int64(last) <= after {
			// A page that does not come after the last one: this server ignores after_id and answers its newest page each time.
			fmt.Fprintf(os.Stderr, "Warning: this Coral cannot page a board's messages; exported its newest %d only.\n", max(len(all), len(page)))
			if len(all) == 0 {
				return page, nil
			}
			return all, nil
		}
		all = append(all, page...)
		after = int64(last)
		if len(page) < exportPageSize {
			return all, nil
		}
	}
	fmt.Fprintf(os.Stderr, "Warning: the export stopped at %d messages; the board has more.\n", len(all))
	return all, nil
}

func cmdExport() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		clierr.Exit1()
	}

	// Parse flags
	outputFile := ""
	mergeFile := ""
	format := "markdown"
	for i, arg := range os.Args {
		if arg == "--output" && i+1 < len(os.Args) {
			outputFile = os.Args[i+1]
		}
		if arg == "--merge" && i+1 < len(os.Args) {
			mergeFile = os.Args[i+1]
		}
		if arg == "--format" && i+1 < len(os.Args) {
			format = os.Args[i+1]
		}
	}
	// Auto-detect format from output file extension
	if format == "markdown" && outputFile != "" {
		if strings.HasSuffix(outputFile, ".html") || strings.HasSuffix(outputFile, ".htm") {
			format = "html"
		} else if strings.HasSuffix(outputFile, ".json") {
			format = "json"
		}
	}

	// Fetch all messages
	messages, err := fetchAllMessages(st.Project, getBoard)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}

	// Convert board messages to exportEntry
	var entries []exportEntry
	for _, m := range messages {
		ts, _ := m["created_at"].(string)
		if len(ts) > 16 {
			ts = ts[:16]
		}
		role, _ := m["job_title"].(string)
		if role == "" {
			role, _ = m["subscriber_id"].(string)
		}
		content, _ := m["content"].(string)
		entries = append(entries, exportEntry{
			Timestamp: ts,
			Role:      role,
			Content:   content,
			Source:    "board",
		})
	}

	// Merge side-chat file if provided
	if mergeFile != "" {
		sideEntries, err := parseMergeFile(mergeFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading merge file: %v\n", err)
			clierr.Exit1()
		}
		entries = append(entries, sideEntries...)
		fmt.Fprintf(os.Stderr, "Merged %d side-chat entries\n", len(sideEntries))
	}

	// Sort all entries by timestamp
	sortEntries(entries)

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No messages to export.")
		os.Exit(0)
	}

	// Get subscribers for context (best-effort: the export still goes out without them)
	var rawSubs []map[string]any
	if subsData, err := getBoard("/" + url.PathEscape(st.Project) + "/subscribers"); err == nil {
		json.Unmarshal(subsData, &rawSubs)
	}
	var subs []exportSubscriber
	for _, s := range rawSubs {
		sid, _ := s["subscriber_id"].(string)
		role, _ := s["job_title"].(string)
		if role == "" {
			role = sid
		}
		subs = append(subs, exportSubscriber{SubscriberID: sid, Role: role})
	}

	// Count by source
	boardCount, sideCount := 0, 0
	for _, e := range entries {
		if e.Source == "board" {
			boardCount++
		} else {
			sideCount++
		}
	}

	// Build canonical export data
	export := exportData{
		Project:     st.Project,
		ExportedAt:  time.Now().Format(time.RFC3339),
		Subscribers: subs,
		Messages:    entries,
		Stats: exportStats{
			Total:    len(entries),
			Board:    boardCount,
			SideChat: sideCount,
		},
	}

	var buf bytes.Buffer
	switch format {
	case "json":
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.Encode(export)
	case "html":
		renderHTML(&buf, &export)
	default:
		renderMarkdown(&buf, &export)
	}

	if outputFile != "" {
		if err := writeExport(outputFile, buf.Bytes()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
		fmt.Printf("Exported %d messages (%s) to %s\n", len(entries), format, outputFile)
	} else {
		fmt.Print(buf.String())
	}
}

func renderMarkdown(buf *bytes.Buffer, d *exportData) {
	buf.WriteString(fmt.Sprintf("# Board Chat Export: %s\n\n", d.Project))
	buf.WriteString(fmt.Sprintf("**Exported**: %s\n", d.ExportedAt))
	buf.WriteString(fmt.Sprintf("**Messages**: %d\n", d.Stats.Total))
	if d.Stats.SideChat > 0 {
		buf.WriteString(fmt.Sprintf("**Board messages**: %d | **Side-chat messages**: %d\n", d.Stats.Board, d.Stats.SideChat))
	}

	if len(d.Subscribers) > 0 {
		buf.WriteString(fmt.Sprintf("**Subscribers**: %d\n\n", len(d.Subscribers)))
		buf.WriteString("| Agent | Role |\n|-------|------|\n")
		for _, s := range d.Subscribers {
			buf.WriteString(fmt.Sprintf("| %s | %s |\n", s.SubscriberID, s.Role))
		}
	}

	buf.WriteString("\n---\n\n## Messages\n\n")

	for _, e := range d.Messages {
		tag := ""
		if e.Source == "side-chat" {
			tag = " 💬 SIDE CHAT"
		}
		buf.WriteString(fmt.Sprintf("**[%s] %s%s:**\n\n", e.Timestamp, e.Role, tag))
		buf.WriteString(e.Content)
		buf.WriteString("\n\n---\n\n")
	}
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

var (
	reCodeBlock  = regexp.MustCompile("(?s)```(\\w*)\\n(.*?)```")
	reHeading    = regexp.MustCompile(`(?m)^(#{1,4})\s+(.+)$`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reUlItem     = regexp.MustCompile(`(?m)^[-*]\s+(.+)$`)
	reOlItem     = regexp.MustCompile(`(?m)^\d+\.\s+(.+)$`)
	reBlockquote = regexp.MustCompile(`(?m)^>\s*(.+)$`)
)

// simpleMarkdownToHTML converts common markdown to HTML for the CLI export.
func simpleMarkdownToHTML(s string) string {
	// Code blocks first (before escaping)
	var codeBlocks []string
	s = reCodeBlock.ReplaceAllStringFunc(s, func(match string) string {
		parts := reCodeBlock.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("\x00CODEBLOCK%d\x00", len(codeBlocks))
		codeBlocks = append(codeBlocks, "<pre><code>"+htmlEscape(parts[2])+"</code></pre>")
		return placeholder
	})

	// Inline code (before escaping)
	var inlineCodes []string
	s = reInlineCode.ReplaceAllStringFunc(s, func(match string) string {
		parts := reInlineCode.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("\x00INLINECODE%d\x00", len(inlineCodes))
		inlineCodes = append(inlineCodes, "<code>"+htmlEscape(parts[1])+"</code>")
		return placeholder
	})

	s = htmlEscape(s)

	// Headings
	s = reHeading.ReplaceAllStringFunc(s, func(match string) string {
		parts := reHeading.FindStringSubmatch(match)
		level := len(parts[1])
		return fmt.Sprintf("<h%d>%s</h%d>", level, parts[2], level)
	})

	// Bold and italic
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "<em>$1</em>")

	// Blockquotes
	s = reBlockquote.ReplaceAllString(s, "<blockquote>$1</blockquote>")

	// Lists (simple single-level)
	s = reUlItem.ReplaceAllString(s, "<li>$1</li>")
	s = reOlItem.ReplaceAllString(s, "<li>$1</li>")

	// Restore code blocks and inline code
	for i, block := range codeBlocks {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00CODEBLOCK%d\x00", i), block)
	}
	for i, code := range inlineCodes {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00INLINECODE%d\x00", i), code)
	}

	// Paragraphs: convert double newlines
	s = strings.ReplaceAll(s, "\n\n", "</p><p>")
	s = "<p>" + s + "</p>"
	s = strings.ReplaceAll(s, "<p></p>", "")

	return s
}

// agentColor returns a consistent HSL color for an agent name.
func agentColor(name string) string {
	h := 0
	for _, c := range name {
		h = (h*31 + int(c)) & 0xFFFFFF
	}
	hue := h % 360
	return fmt.Sprintf("hsl(%d, 60%%, 45%%)", hue)
}

func renderHTML(buf *bytes.Buffer, d *exportData) {
	buf.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Board Chat Export: ` + htmlEscape(d.Project) + `</title>
<style>
  :root { --bg: #0d1117; --surface: #161b22; --border: #30363d; --text: #e6edf3; --muted: #8b949e; --accent: #58a6ff; --side-chat: #1a1a2e; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); line-height: 1.6; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .stats { color: var(--muted); margin-bottom: 1.5rem; font-size: 0.9rem; }
  .stats span { margin-right: 1.5rem; }
  .subscribers { margin-bottom: 2rem; }
  .subscribers summary { cursor: pointer; color: var(--accent); font-weight: 600; margin-bottom: 0.5rem; }
  .sub-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; padding: 0.5rem 0; }
  .sub-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 0.4rem 0.8rem; font-size: 0.85rem; }
  .sub-chip .role { font-weight: 600; }
  .messages { display: flex; flex-direction: column; gap: 0.75rem; }
  .msg { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1rem; border-left: 3px solid var(--accent); }
  .msg.side-chat { background: var(--side-chat); border-left-color: #f0883e; }
  .msg-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; }
  .msg-role { font-weight: 700; font-size: 0.95rem; }
  .msg-time { color: var(--muted); font-size: 0.8rem; }
  .msg-tag { background: #f0883e33; color: #f0883e; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px; font-weight: 600; }
  .msg-content { word-wrap: break-word; font-size: 0.9rem; }
  .msg-content p { margin: 0.4em 0; }
  .msg-content h1, .msg-content h2, .msg-content h3, .msg-content h4 { margin: 0.8em 0 0.3em; }
  .msg-content code { background: #1a1e24; padding: 0.15rem 0.4rem; border-radius: 3px; font-size: 0.85em; }
  .msg-content pre { background: #1a1e24; padding: 0.8rem; border-radius: 6px; overflow-x: auto; margin: 0.5rem 0; }
  .msg-content ul, .msg-content ol { padding-left: 1.5em; margin: 0.3em 0; }
  .msg-content li { margin: 0.2em 0; }
  .msg-content blockquote { border-left: 3px solid var(--border); padding-left: 0.8em; color: var(--muted); margin: 0.5em 0; }
  .msg-content strong { font-weight: 700; }
  .msg-content table { border-collapse: collapse; margin: 0.5em 0; width: 100%; }
  .msg-content th, .msg-content td { border: 1px solid var(--border); padding: 0.4em 0.6em; text-align: left; font-size: 0.85em; }
  .msg-content pre code { background: none; padding: 0; }
</style>
</head>
<body>
<div class="container">
`)
	buf.WriteString(fmt.Sprintf("<h1>%s</h1>\n", htmlEscape(d.Project)))
	buf.WriteString("<div class=\"stats\">\n")
	buf.WriteString(fmt.Sprintf("  <span>Exported: %s</span>\n", d.ExportedAt))
	buf.WriteString(fmt.Sprintf("  <span>Messages: %d</span>\n", d.Stats.Total))
	if d.Stats.SideChat > 0 {
		buf.WriteString(fmt.Sprintf("  <span>Board: %d</span>\n", d.Stats.Board))
		buf.WriteString(fmt.Sprintf("  <span>Side-chat: %d</span>\n", d.Stats.SideChat))
	}
	buf.WriteString("</div>\n")

	if len(d.Subscribers) > 0 {
		buf.WriteString("<details class=\"subscribers\">\n")
		buf.WriteString(fmt.Sprintf("  <summary>%d Subscribers</summary>\n", len(d.Subscribers)))
		buf.WriteString("  <div class=\"sub-grid\">\n")
		for _, s := range d.Subscribers {
			buf.WriteString(fmt.Sprintf("    <div class=\"sub-chip\"><span class=\"role\">%s</span></div>\n", htmlEscape(s.Role)))
		}
		buf.WriteString("  </div>\n</details>\n")
	}

	buf.WriteString("<div class=\"messages\">\n")
	for _, e := range d.Messages {
		cls := "msg"
		if e.Source == "side-chat" {
			cls = "msg side-chat"
		}
		color := agentColor(e.Role)
		buf.WriteString(fmt.Sprintf("<div class=\"%s\">\n", cls))
		buf.WriteString("  <div class=\"msg-header\">\n")
		buf.WriteString(fmt.Sprintf("    <span class=\"msg-role\" style=\"color: %s\">%s</span>\n", color, htmlEscape(e.Role)))
		buf.WriteString(fmt.Sprintf("    <span class=\"msg-time\">%s</span>\n", htmlEscape(e.Timestamp)))
		if e.Source == "side-chat" {
			buf.WriteString("    <span class=\"msg-tag\">SIDE CHAT</span>\n")
		}
		buf.WriteString("  </div>\n")
		buf.WriteString(fmt.Sprintf("  <div class=\"msg-content\">%s</div>\n", simpleMarkdownToHTML(e.Content)))
		buf.WriteString("</div>\n")
	}
	buf.WriteString("</div>\n</div>\n</body>\n</html>\n")
}

// sortEntries sorts by timestamp string (ISO format sorts lexicographically).
func sortEntries(entries []exportEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Timestamp < entries[j-1].Timestamp; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// writeExport writes a board export to path, readable by this user only (#4381, Security's #4371):
// it holds the board's messages. A file that was already there is made so too.
func writeExport(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
