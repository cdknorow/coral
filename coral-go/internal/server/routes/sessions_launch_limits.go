// What an agent token may launch: flag limits and the saved team template (moved out of sessions_launch.go, #5003; no behaviour change).

package routes

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	setreg "github.com/subgentic/coral/internal/settings"
)

// agentCaller reports a request made with an agent session's token.
func agentCaller(r *http.Request) bool {
	_, ok := VerifiedSession(r.Context())
	return ok
}

// limitAgentLaunch is what an agent may ask for when it starts another agent (#4032, Security's
// #4031 finding 1): users govern agents through their CLI's permissions, so an agent must not
// start one with fewer. Only the flags in agentLaunchFlags are kept (an allowlist, so a spelling
// nobody listed is removed too); allowed tools, capability "allow" rules and custom hooks
// (commands the agent's CLI would run itself) are removed. Deny rules, which only narrow, stay.
// The Operator (the dashboard, the command line, the API key) keeps every choice.
func limitAgentLaunch(route string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}) ([]string, *agent.Capabilities, []string, map[string]interface{}) {
	clean := keepAgentLaunchFlags(flags)
	var dropped []string
	if len(clean) != len(flags) {
		dropped = append(dropped, "flags")
	}
	if caps != nil && len(caps.Allow) > 0 {
		dropped = append(dropped, "capability allow rules")
		caps = &agent.Capabilities{Deny: caps.Deny}
	}
	if len(tools) > 0 {
		dropped = append(dropped, "allowed tools")
		tools = nil
	}
	if len(hooks) > 0 {
		dropped = append(dropped, "hooks")
		hooks = nil
	}
	if len(dropped) > 0 {
		log.Printf("[%s] an agent's launch asked for %s: removed (only the Operator can start a less-restricted agent)", route, strings.Join(dropped, ", "))
	}
	return clean, caps, tools, hooks
}

// agentLaunchFlags are the only flags an agent's launch keeps (value: the flag takes the next
// word when written without "="). coral-agent launch passes nothing else (--model is its only
// CLI option); everything a CLI could be widened with, in any spelling, is not here.
var agentLaunchFlags = map[string]bool{"--model": true}

// keepAgentLaunchFlags keeps agentLaunchFlags, as "--flag", "--flag=value" or "--flag value",
// and drops every other word. A value that looks like a flag is refused with its flag.
func keepAgentLaunchFlags(flags []string) []string {
	var out []string
	for i := 0; i < len(flags); i++ {
		name, value, hasValue := strings.Cut(flags[i], "=")
		takesValue, ok := agentLaunchFlags[name]
		if !ok {
			continue
		}
		if !takesValue {
			out = append(out, flags[i])
			continue
		}
		if !hasValue {
			if i+1 >= len(flags) {
				continue
			}
			i++
			value = flags[i]
		}
		if value == "" || strings.HasPrefix(value, "-") {
			continue
		}
		out = append(out, name, value)
	}
	return out
}

// inSavedTeamTemplate reports whether a team member's definition is exactly an agent entry of
// a team template the Operator saved, after normalising (flags as words, JSON key order), so
// an agent launching that template gets what the Operator chose (#4032), and so does a Coral Hub
// user (#126 F4). Exactly: every field, its knowledge text included (the agent reads it as it
// reads its prompt). The templates are read from this computer's settings, which neither an
// agent's token nor the tunnel can write; nothing in the request says which template it is.
// agentType is the type the member will run as (its own, else the team's): capabilities mean a
// different thing for each CLI (Claude allow rules become agy's or Codex's bypass), so a saved
// member matches only with an agent_type equal to it, and one saved with no agent_type never
// matches (the settings don't record what the owner ran it as) (#126 F4 review F1).
func inSavedTeamTemplate(settings map[string]string, def launchTeamAgent, agentType string) bool {
	var templates []struct {
		Agents []struct {
			Name         string                 `json:"name"`
			Role         string                 `json:"role"`
			Prompt       string                 `json:"prompt"`
			Description  string                 `json:"description"`
			Capabilities *agent.Capabilities    `json:"capabilities"`
			AgentType    string                 `json:"agent_type"`
			Model        string                 `json:"model"`
			Flags        json.RawMessage        `json:"flags"`
			Tools        []string               `json:"tools"`
			Hooks        map[string]interface{} `json:"hooks"`
			Knowledge    string                 `json:"knowledge"`
		} `json:"agents"`
	}
	if json.Unmarshal([]byte(setreg.Raw(settings, "saved_team_templates")), &templates) != nil {
		return false
	}
	if agentType == "" {
		return false
	}
	want := teamAgentKey(def.Name, def.Prompt, agentType, def.Model, def.Flags, def.Capabilities, def.Tools, def.Hooks, def.Knowledge)
	for _, t := range templates {
		for _, a := range t.Agents {
			if a.AgentType == "" {
				continue
			}
			name, prompt := a.Name, a.Prompt
			if name == "" {
				name = a.Role // the dashboard's older template fields
			}
			if prompt == "" {
				prompt = a.Description
			}
			var flags []string
			var asText string
			if len(a.Flags) == 0 || string(a.Flags) == "null" {
				// no flags key: no flags (it used to fail both reads below, so such an entry never matched)
			} else if json.Unmarshal(a.Flags, &asText) == nil {
				flags = strings.Fields(asText)
			} else if json.Unmarshal(a.Flags, &flags) != nil {
				continue
			}
			if teamAgentKey(name, prompt, a.AgentType, a.Model, flags, a.Capabilities, a.Tools, a.Hooks, a.Knowledge) == want {
				return true
			}
		}
	}
	return false
}

// teamAgentKey is a team member's definition in one canonical JSON form.
func teamAgentKey(name, prompt, agentType, model string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}, knowledge string) string {
	var words []string
	for _, f := range flags {
		words = append(words, strings.Fields(f)...)
	}
	if caps.IsEmpty() {
		caps = nil
	}
	if len(tools) == 0 {
		tools = nil
	}
	if len(hooks) == 0 {
		hooks = nil
	}
	b, _ := json.Marshal([]any{name, prompt, agentType, model, words, caps, tools, hooks, knowledge}) // maps marshal with sorted keys
	return string(b)
}

func stripAgentPermissionFlags(flags []string) []string {
	agentPermFlags := map[string]bool{
		"--full-auto": true, "--dangerously-skip-permissions": true,
		"--dangerously-bypass-approvals-and-sandbox": true,
		"--sandbox": true, "--search": true,
		"--approval-mode": true, "--permission-mode": true, "--yolo": true,
		"--mode": true,
	}

	var cleanFlags []string
	skipNext := false
	for _, f := range flags {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(f, "--permission-mode=") || strings.HasPrefix(f, "--mode=") || strings.HasPrefix(f, "--sandbox=") || strings.HasPrefix(f, "--approval-mode=") {
			continue
		}
		if agentPermFlags[f] {
			// Some flags take a value argument (e.g. --sandbox workspace-write, -a untrusted, --mode plan).
			if f == "--sandbox" || f == "--approval-mode" || f == "--permission-mode" || f == "-a" || f == "--mode" {
				skipNext = true
			}
			continue
		}
		if f == "-a" {
			skipNext = true
			continue
		}
		cleanFlags = append(cleanFlags, f)
	}
	return cleanFlags
}

// agentLaunchUnsupervisedCode is the code of the refusal of an agent starting a type that never asks (#5901, Security's ruling on #4032).
const agentLaunchUnsupervisedCode = "agent_launch_unsupervised"

// agentLaunchUnsupervised says whether an agent may not start agentType because it never asks before running tools: the type skips the prompts with no flags at all
// (agent.SkipsPermissionPrompts, which is Pi today, so a future CLI without prompts is covered the same way). An agent never starts an agent that asks for less than itself
// (#4032), and the only things it may choose are the model flags; such a type has no mode that asks to fall back to, so it would be a way round the stripped permission flags.
func agentLaunchUnsupervised(agentType string) bool {
	return agent.SkipsPermissionPrompts(agentType, nil)
}

func agentLaunchUnsupervisedMessage(agentType string) string {
	return "an agent cannot start a " + agentType + " agent: " + agentType + " does not ask before running tools; ask the Operator to start it"
}
