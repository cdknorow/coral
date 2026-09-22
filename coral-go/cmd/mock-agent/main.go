// mock-agent is a lightweight stand-in for the Claude CLI used in stress tests.
// It accepts the same flags as claude (--session-id, --settings, prompt file)
// and runs a heartbeat loop that posts to and reads from the message board via
// the Coral HTTP API. This keeps the tmux session alive and exercises the board.
// Each line typed into it is echoed as "[mock-agent] input: ..." for tests.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Parse flags loosely — we only need to consume them so the process doesn't error.
	var sessionID, settingsFile string
	var positionalArgs []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--resume":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--settings":
			if i+1 < len(args) {
				settingsFile = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				positionalArgs = append(positionalArgs, args[i])
			}
		}
	}

	// Extract board name and env vars from the settings JSON written by the server
	boardName, settingsEnv := parseSettings(settingsFile)

	// Fallback: search positional args for board name (PTY backend expands
	// $(cat prompt.txt) so the prompt text arrives as a positional arg)
	if boardName == "" {
		for _, arg := range positionalArgs {
			if found := extractBoard(arg); found != "" {
				boardName = found
				break
			}
		}
	}

	// Apply env vars from settings (CORAL_SESSION_NAME, CORAL_PORT, etc.)
	// Settings env overrides process env only if not already set.
	for k, v := range settingsEnv {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}

	sessionName := os.Getenv("CORAL_SESSION_NAME")
	port := os.Getenv("CORAL_PORT")
	if port == "" {
		port = "8420"
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Log startup
	fmt.Fprintf(os.Stderr, "[mock-agent] started session_id=%s session_name=%s board=%s port=%s\n",
		short(sessionID), sessionName, boardName, port)

	// Post an introduction
	if boardName != "" {
		post(baseURL, boardName, sessionName,
			fmt.Sprintf("Mock agent online (session=%s)", short(sessionID)))
	}

	// Echo every line typed into the agent (what Coral sends: prompts, task
	// notifications) with a fixed prefix, so tests can check the pane for the
	// exact input.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Printf("[mock-agent] input: %s\n", sc.Text())
		}
	}()

	// Heartbeat loop: post & read every 5s
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	count := 0
	for {
		select {
		case <-sig:
			fmt.Fprintf(os.Stderr, "[mock-agent] shutting down session=%s\n", short(sessionID))
			return
		case <-ticker.C:
			count++
			// Read messages (exercises the read path)
			if boardName != "" {
				msgs := readMessages(baseURL, boardName)
				fmt.Fprintf(os.Stderr, "[mock-agent] heartbeat #%d board=%s msgs=%d\n", count, boardName, msgs)
			}
			// Post a heartbeat every 3rd tick
			if boardName != "" && count%3 == 0 {
				post(baseURL, boardName, sessionName,
					fmt.Sprintf("heartbeat #%d from %s", count, short(sessionID)))
			}
			// Print to stdout so tmux pipe-pane captures activity
			fmt.Printf("[%s] heartbeat #%d\n", time.Now().Format("15:04:05"), count)
		}
	}
}

// extractBoard searches text for a board name pattern like: board "NAME" or message board "NAME"
func extractBoard(text string) string {
	for _, prefix := range []string{`message board "`, `board "`} {
		if idx := strings.Index(text, prefix); idx >= 0 {
			rest := text[idx+len(prefix):]
			if end := strings.Index(rest, `"`); end >= 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

// parseSettings reads the settings JSON and extracts the board name from the
// system prompt and any env vars from the env block.
func parseSettings(path string) (boardName string, env map[string]string) {
	env = make(map[string]string)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return
	}

	// Extract env vars
	if envBlock, ok := settings["env"].(map[string]any); ok {
		for k, v := range envBlock {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
	}

	// Extract board name from system prompt
	if sp, ok := settings["systemPrompt"].(string); ok {
		boardName = extractBoard(sp)
	}
	return
}

func post(baseURL, board, sessionName, message string) {
	subscriberID := os.Getenv("CORAL_SUBSCRIBER_ID")
	if subscriberID == "" {
		subscriberID = sessionName
	}
	body, _ := json.Marshal(map[string]string{
		"subscriber_id": subscriberID,
		"content":       message,
	})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/messages", baseURL, board), "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	resp.Body.Close()
}

func readMessages(baseURL, board string) int {
	resp, err := http.Get(fmt.Sprintf("%s/api/board/%s/messages", baseURL, board))
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var msgs []any
	json.Unmarshal(data, &msgs)
	return len(msgs)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
