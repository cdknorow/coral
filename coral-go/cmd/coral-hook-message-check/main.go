// Command coral-hook-message-check runs after each tool use to check for
// unread message board messages. If any exist, it prints a notification
// so the agent sees it during its workflow.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/tmux"
	"io"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/subgentic/coral/internal/hooks"
)

func main() {
	hooks.InstallIdentity("coral-hook-message-check") // the agent session's token, sent with every request (#3387)
	defer func() {
		if r := recover(); r != nil {
			hooks.LogCrash(r)
		}
	}()

	// Read and discard stdin (hook protocol requires it)
	io.Copy(io.Discard, os.Stdin) // never held in memory

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
	// A state file's server_url only if it's this agent's own Coral (#3581).
	base := hooks.CoralBase()
	if v, ok := state["server_url"].(string); ok && v != "" {
		if hooks.TrustedServerURL(v, base) {
			base = strings.TrimRight(strings.TrimSpace(v), "/")
		} else {
			hooks.DebugLog("message_check: ignored a board state server_url that isn't loopback or this agent's Coral")
		}
	}

	resp, err := hooks.CoralAPI(base, "GET",
		// Escaped: a role like "QA 1" or "Backend Dev" made an invalid URL, and the check
		// silently never ran.
		fmt.Sprintf("/api/board/%s/messages/check?subscriber_id=%s", url.PathEscape(project), url.QueryEscape(subscriberID)), nil)
	if err != nil {
		return
	}

	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		return
	}

	count, _ := result["unread"].(float64)
	hooks.DebugLog(fmt.Sprintf("message_check: project=%s unread=%d", hooks.LogID(project), int(count)))

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
		out, err := exec(tmux.BinaryFromEnv(), "display-message", "-p", "#S")
		if err == nil {
			sessionName = strings.TrimSpace(out)
		}
	}
	// No session, no board: never the hostname, which every sessionless process shared, so
	// it would report whichever board one of them joined (#3864).
	if sessionName == "" {
		return nil
	}

	// Must match coral-board's stateFilePath: the CLI writes this file into the
	// launching server's data directory, so reading ~/.coral unconditionally
	// would look in the wrong place for any server started with --home.
	statePath := filepath.Join(hooks.CoralDir(), hooks.BoardStateFileName(sessionName))
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
