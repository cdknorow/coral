// Command coral-hook-session-start reads the Claude Code SessionStart hook
// JSON from stdin, extracts the model field, and updates the live session's
// context window via the Coral API.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/subgentic/coral/internal/hooks"
)

func main() {
	hooks.InstallIdentity("coral-hook-session-start") // the agent session's token, sent with every request (#3387)
	defer func() {
		if r := recover(); r != nil {
			hooks.LogCrash(r)
		}
	}()

	raw, ok, err := hooks.ReadStdin(os.Stdin)
	if err != nil {
		hooks.DebugLog(fmt.Sprintf("SESSION_START STDIN_ERROR: %v", err))
		return
	}
	if !ok {
		hooks.DebugLog("SESSION_START DROPPED: the payload is over the size bound")
		return
	}

	hooks.DebugLog(fmt.Sprintf("SESSION_START RAW(%d)", len(raw))) // the size only: never the payload (#3571)

	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		hooks.DebugLog("SESSION_START JSON_ERROR")
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

	hooks.CoralAPI(base, "POST", hooks.LivePath(agentName, "/context-window"), payload)
	hooks.DebugLog(fmt.Sprintf("SESSION_START DONE: agent=%s model=%s", hooks.LogID(agentName), hooks.LogID(model)))
}
