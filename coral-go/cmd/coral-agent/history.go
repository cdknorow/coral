package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func printHistoryUsage() {
	fmt.Println(`coral-agent history - search your conversation history and inspect context

Commands:
  history search <query> [flags]         Search your earlier assistant and user messages
  history context [session] <idx> [flags] Retrieve conversation turns surrounding a message
  search <query> [flags]                 Alias for 'history search'

Flags for 'history search':
  --roles R       Comma-separated roles to search (default: "assistant,user")
  --limit N       Maximum results to return (default: 10, max: 100)
  --offset N      Number of results to skip (default: 0)
  --ancestors     Include proven resumed ancestors (default: true, use --ancestors=false to disable)
  --session S     Explicit session ID to search (must be in your proven lineage)
  --json          Output raw JSON response

Flags for 'history context':
  --window W      Number of messages before/after target turn (default: 2, max: 10)
  --json          Output raw JSON response`)
}

func cmdHistory(args []string) {
	if len(args) == 0 {
		printHistoryUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "search":
		cmdHistorySearch(args[1:])
	case "context":
		cmdHistoryContext(args[1:])
	case "--help", "-h", "help":
		printHistoryUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown history subcommand: %s\n", args[0])
		printHistoryUsage()
		os.Exit(1)
	}
}

func cmdHistorySearch(args []string) {
	// Reorder args so query can appear before or after flags
	var flagArgs []string
	var queryParts []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.Contains(args[i], "=") {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else {
			queryParts = append(queryParts, args[i])
		}
	}

	fs := flag.NewFlagSet("history-search", flag.ExitOnError)
	roles := fs.String("roles", "assistant,user", "Roles to search (assistant, user)")
	limit := fs.Int("limit", 10, "Maximum number of results (max 100)")
	offset := fs.Int("offset", 0, "Number of results to skip")
	ancestors := fs.Bool("ancestors", true, "Include proven resumed ancestor sessions")
	targetSession := fs.String("session", "", "Target session ID in lineage")
	rawJSON := fs.Bool("json", false, "Output JSON response")
	fs.Parse(flagArgs)

	query := strings.Join(queryParts, " ")
	if strings.TrimSpace(query) == "" {
		fmt.Fprintln(os.Stderr, "Error: search query is required.")
		fmt.Fprintln(os.Stderr, "Usage: coral-agent history search <query> [flags]")
		os.Exit(1)
	}

	sid := resolveSessionID()

	params := url.Values{}
	params.Set("session_id", sid)
	params.Set("query", query)
	params.Set("roles", *roles)
	params.Set("limit", strconv.Itoa(*limit))
	params.Set("offset", strconv.Itoa(*offset))
	params.Set("include_ancestors", strconv.FormatBool(*ancestors))
	if *targetSession != "" {
		params.Set("target_session_id", *targetSession)
	}

	data, status, err := apiCallRaw("GET", "/history/search?"+params.Encode(), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Coral server: %v\n", err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			fmt.Fprintf(os.Stderr, "Search error (%d): %s\n", status, errResp.Error)
		} else {
			fmt.Fprintf(os.Stderr, "Search error (%d): %s\n", status, string(data))
		}
		os.Exit(1)
	}

	if *rawJSON {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(data))
		}
		return
	}

	var resp struct {
		Query        string `json:"query"`
		Status       string `json:"status"`
		TotalMatches int    `json:"total_matches"`
		HasMore      bool   `json:"has_more"`
		Limit        int    `json:"limit"`
		Offset       int    `json:"offset"`
		Results      []struct {
			SessionID    string `json:"session_id"`
			IsCurrent    bool   `json:"is_current"`
			MessageIndex int    `json:"message_index"`
			Role         string `json:"role"`
			Timestamp    string `json:"timestamp"`
			Excerpt      string `json:"excerpt"`
		} `json:"results"`
		SessionsSearched []struct {
			SessionID string `json:"session_id"`
			Role      string `json:"role"`
			Status    string `json:"status"`
			Error     string `json:"error,omitempty"`
		} `json:"sessions_searched"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding search response: %v\n", err)
		os.Exit(1)
	}

	if resp.TotalMatches == 0 {
		fmt.Printf("No matches found for %q in your session history.\n", resp.Query)
		if resp.Status == "unavailable" || resp.Status == "partial" {
			fmt.Println("\nNote: Some sessions could not be searched because their transcripts are unavailable.")
		}
		return
	}

	statusSuffix := ""
	if resp.Status == "partial" {
		statusSuffix = " (partial: some ancestor transcripts unavailable)"
	}

	fmt.Printf("Found %d matches for %q%s:\n\n", resp.TotalMatches, resp.Query, statusSuffix)

	for i, m := range resp.Results {
		sessLabel := "ancestor"
		if m.IsCurrent {
			sessLabel = "current"
		}

		timeStr := m.Timestamp
		if timeStr == "" {
			timeStr = "unknown time"
		}

		displayNum := resp.Offset + i + 1
		fmt.Printf("[%d] session: %s (%s) | turn: #%d | role: %s | %s\n",
			displayNum, m.SessionID, sessLabel, m.MessageIndex, m.Role, timeStr)
		fmt.Printf("    %s\n", m.Excerpt)
		fmt.Printf("    Context hint: coral-agent history context %s %d\n\n", m.SessionID, m.MessageIndex)
	}

	if resp.HasMore {
		nextOffset := resp.Offset + len(resp.Results)
		fmt.Printf("More results available. Run: coral-agent history search %q --offset %d\n", resp.Query, nextOffset)
	}
}

func cmdHistoryContext(args []string) {
	var flagArgs []string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.Contains(args[i], "=") {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else {
			posArgs = append(posArgs, args[i])
		}
	}

	fs := flag.NewFlagSet("history-context", flag.ExitOnError)
	window := fs.Int("window", 2, "Surrounding turns before and after target turn (default: 2, max: 10)")
	rawJSON := fs.Bool("json", false, "Output JSON response")
	fs.Parse(flagArgs)

	sid := resolveSessionID()
	targetSession := sid
	targetIndex := -1

	if len(posArgs) == 1 {
		idx, err := strconv.Atoi(posArgs[0])
		if err != nil || idx < 0 {
			fmt.Fprintln(os.Stderr, "Error: turn index must be a non-negative integer.")
			fmt.Fprintln(os.Stderr, "Usage: coral-agent history context [session_id] <turn_index> [--window W]")
			os.Exit(1)
		}
		targetIndex = idx
	} else if len(posArgs) >= 2 {
		targetSession = posArgs[0]
		idx, err := strconv.Atoi(posArgs[1])
		if err != nil || idx < 0 {
			fmt.Fprintln(os.Stderr, "Error: turn index must be a non-negative integer.")
			fmt.Fprintln(os.Stderr, "Usage: coral-agent history context [session_id] <turn_index> [--window W]")
			os.Exit(1)
		}
		targetIndex = idx
	} else {
		fmt.Fprintln(os.Stderr, "Usage: coral-agent history context [session_id] <turn_index> [--window W]")
		os.Exit(1)
	}

	params := url.Values{}
	params.Set("session_id", sid)
	params.Set("target_session_id", targetSession)
	params.Set("message_index", strconv.Itoa(targetIndex))
	params.Set("window", strconv.Itoa(*window))

	data, status, err := apiCallRaw("GET", "/history/context?"+params.Encode(), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Coral server: %v\n", err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			fmt.Fprintf(os.Stderr, "Context error (%d): %s\n", status, errResp.Error)
		} else {
			fmt.Fprintf(os.Stderr, "Context error (%d): %s\n", status, string(data))
		}
		os.Exit(1)
	}

	if *rawJSON {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(data))
		}
		return
	}

	var resp struct {
		SessionID     string `json:"session_id"`
		TargetIndex   int    `json:"target_index"`
		TotalMessages int    `json:"total_messages"`
		Window        int    `json:"window"`
		Messages      []struct {
			MessageIndex int    `json:"message_index"`
			Role         string `json:"role"`
			Timestamp    string `json:"timestamp"`
			Content      string `json:"content"`
			IsTarget     bool   `json:"is_target"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding context response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Context for session %s around turn #%d (window: %d, total session turns: %d):\n\n",
		resp.SessionID, resp.TargetIndex, resp.Window, resp.TotalMessages)

	for _, m := range resp.Messages {
		prefix := "  "
		tag := ""
		if m.IsTarget {
			prefix = ">>"
			tag = " [TARGET MATCH]"
		}
		timeStr := m.Timestamp
		if timeStr == "" {
			timeStr = "unknown time"
		}
		fmt.Printf("%s [Turn #%d] %s (%s)%s:\n", prefix, m.MessageIndex, m.Role, timeStr, tag)
		lines := strings.Split(m.Content, "\n")
		for _, line := range lines {
			fmt.Printf("   %s\n", line)
		}
		fmt.Println()
	}
}
