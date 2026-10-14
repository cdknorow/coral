// Command coral-hook-session-start reads the Claude Code SessionStart hook
// JSON from stdin, extracts the model field, and updates the live session's
// context window via the Coral API.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"

	"github.com/cdknorow/coral/internal/hooks"
)

func main() {
	hooks.InstallIdentity("coral-hook-session-start") // the agent session's token, sent with every request (#3387)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CRASH] hook panicked: %v\n%s", r, debug.Stack())
		}
	}()

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		hooks.DebugLog(fmt.Sprintf("SESSION_START STDIN_ERROR: %v", err))
		return
	}

	hooks.DebugLog(fmt.Sprintf("SESSION_START RAW(%d): %s", len(raw), hooks.Truncate(string(raw), 300)))

	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		hooks.DebugLog(fmt.Sprintf("SESSION_START JSON_ERROR: %v", err))
		return
	}

	model := hooks.StrVal(d, "model")
	if model == "" {
		hooks.DebugLog("SESSION_START DROPPED: no model field")
		return
	}

	sessionID := hooks.ResolveSessionID(hooks.StrVal(d, "session_id"))
	agentName := hooks.ResolveAgentName(d)
	if agentName == "" {
		hooks.DebugLog("SESSION_START DROPPED: no agent_name")
		return
	}

	base := hooks.CoralBase()
	payload := map[string]any{
		"session_id": sessionID,
		"model":      model,
	}

	hooks.CoralAPI(base, "POST", fmt.Sprintf("/api/sessions/live/%s/context-window", agentName), payload)
	hooks.DebugLog(fmt.Sprintf("SESSION_START DONE: agent=%s model=%s", agentName, model))
}
