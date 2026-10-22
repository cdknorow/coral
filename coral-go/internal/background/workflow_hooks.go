package background

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/subgentic/coral/internal/agentprio"

	"github.com/subgentic/coral/internal/agentenv"
)

// parseStepHooks unmarshals the hooks JSON from a step definition.
func parseStepHooks(raw json.RawMessage) map[string]interface{} {
	if raw == nil {
		return nil
	}
	var hooks map[string]interface{}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil
	}
	return hooks
}

// claudeNativeEvents are hook events that Claude Code handles natively via settings.json.
var claudeNativeEvents = map[string]bool{
	"PreToolUse":   true,
	"PostToolUse":  true,
	"Stop":         true,
	"Notification": true,
	"SubagentStop": true,
}

// filterHooksForClaude returns only Claude-native events from the hooks map.
func filterHooksForClaude(hooks map[string]interface{}) map[string]interface{} {
	if hooks == nil {
		return nil
	}
	filtered := make(map[string]interface{})
	for event, groups := range hooks {
		if claudeNativeEvents[event] {
			filtered[event] = groups
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// hookDir is where a step's hook commands run: the workflow's repository, as
// its steps do, or the step's own run directory when the workflow has none.
func hookDir(repoPath, stepDir string) string {
	if repoPath != "" {
		return repoPath
	}
	return stepDir
}

// fireHooks executes hook commands for a given event. Best-effort: hook failures
// are logged but don't fail the step. Timeout per hook: 60s (matches Claude Code default).
// Each hook runs in dir, in its own process group, which its timeout kills whole.
func (wr *WorkflowRunner) fireHooks(ctx context.Context, hooks map[string]interface{}, event, dir string, env []string) {
	eventHooks, ok := hooks[event]
	if !ok {
		return
	}
	groups, ok := eventHooks.([]interface{})
	if !ok {
		return
	}

	for _, g := range groups {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		hookList, ok := group["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hookList {
			hook, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			command, _ := hook["command"].(string)
			if command == "" {
				continue
			}

			shell, serr := workflowShell()
			if serr != nil {
				wr.logger.Warn("hook execution failed", "event", event, "command", command, "error", serr)
				continue
			}
			hookCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			cmd := exec.CommandContext(hookCtx, shell, "-c", command)
			cmd.Dir = dir
			cmd.Env = append(agentenv.Filter(os.Environ()), env...) // #3389: no server-only settings
			configureChildProcess(cmd)
			err := cmd.Start()
			if err == nil {
				trackChildProcess(cmd)
				agentprio.ApplyGroup(cmd.Process.Pid) // #3437
				err = cmd.Wait()
				releaseChildProcess(cmd)
			}
			if err != nil {
				wr.logger.Warn("hook execution failed", "event", event, "command", command, "error", err)
			}
			cancel()
		}
	}
}
