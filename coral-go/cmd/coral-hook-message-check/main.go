// Command coral-hook-message-check runs after each tool use to check for
// unread message board messages. If any exist, it prints a notification
// so the agent sees it during its workflow.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/cdknorow/coral/internal/hooks"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CRASH] hook panicked: %v\n%s", r, debug.Stack())
		}
	}()

	// Read and discard stdin (hook protocol requires it)
	io.ReadAll(os.Stdin)

	state := loadBoardState()
	if state == nil {
		return
	}

	project, _ := state["project"].(string)
	if project == "" {
		return
	}

	// Resolve subscriber_id: env var > state file job_title
	subscriberID := os.Getenv("CORAL_SUBSCRIBER_ID")
	if subscriberID == "" {
		subscriberID, _ = state["job_title"].(string)
	}
	if subscriberID == "" {
		return
	}

	// Server resolution: state file > CORAL_URL env > localhost fallback
	base := ""
	if v, ok := state["server_url"].(string); ok && v != "" {
		base = strings.TrimRight(v, "/")
	}
	if base == "" {
		base = hooks.CoralBase()
	}

	resp, err := hooks.CoralAPI(base, "GET",
		fmt.Sprintf("/api/board/%s/messages/check?subscriber_id=%s", project, subscriberID), nil)
	if err != nil {
		return
	}

	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		return
	}

	count, _ := result["unread"].(float64)
	hooks.DebugLog(fmt.Sprintf("message_check: project=%s unread=%d", project, int(count)))

	if int(count) > 0 {
		plural := "s"
		if int(count) == 1 {
			plural = ""
		}
		fmt.Printf("\nYou have %d unread message%s on the message board. Run 'coral-board read' to see them.\n\n",
			int(count), plural)
	}
}

func loadBoardState() map[string]any {
	sessionName := os.Getenv("CORAL_SESSION_NAME")
	if sessionName == "" && os.Getenv("TMUX") != "" {
		out, err := exec("tmux", "display-message", "-p", "#S")
		if err == nil {
			sessionName = strings.TrimSpace(out)
		}
	}
	if sessionName == "" {
		host, _ := os.Hostname()
		sessionName = host
	}
	if sessionName == "" {
		return nil
	}

	safeName := strings.NewReplacer("/", "_", "\\", "_").Replace(sessionName)
	// Must match coral-board's stateFilePath: the CLI writes this file into the
	// launching server's data directory, so reading ~/.coral unconditionally
	// would look in the wrong place for any server started with --home.
	statePath := filepath.Join(coralDir(), fmt.Sprintf("board_state_%s.json", safeName))
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}

	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return state
}

func exec(name string, args ...string) (string, error) {
	if runtime.GOOS == "windows" {
		// tmux not available on Windows — skip
		return "", fmt.Errorf("tmux not available on Windows")
	}
	cmd := osexec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

// coralDir returns the data directory of the Coral server that launched this
// agent, falling back to ~/.coral. Coral sets CORAL_DATA_DIR in every agent's
// environment; see agent.CoralEnv.
func coralDir() string {
	for _, k := range []string{"CORAL_DATA_DIR", "CORAL_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".coral")
}
