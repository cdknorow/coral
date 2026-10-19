package agentmodels

import "strings"

// The thinking (reasoning effort) level an agent is started with (#4969). Each CLI takes it its own way and the levels differ per
// model (the CLI clamps a level its model does not take), so only what a CLI documents is passed, and only for the types listed
// here: others are added as each is checked by hand.
//
//   - claude: --effort low|medium|high|xhigh|max (claude --help).
var thinkingLevels = map[string][]string{
	"claude": {"low", "medium", "high", "xhigh", "max"},
}

// ThinkingLevels returns the levels Coral passes to agentType's CLI, nil when that type has none yet.
func ThinkingLevels(agentType string) []string { return thinkingLevels[agentType] }

// ValidThinking reports whether level is one Coral passes to agentType's CLI. "" (the CLI's own default) is valid for every type.
func ValidThinking(agentType, level string) bool {
	if level == "" {
		return true
	}
	for _, l := range thinkingLevels[agentType] {
		if l == level {
			return true
		}
	}
	return false
}

// ThinkingArgs is the launch arguments that give agentType's CLI the level: nil for "" or a type with no level yet.
func ThinkingArgs(agentType, level string) []string {
	if level == "" || !ValidThinking(agentType, level) {
		return nil
	}
	switch agentType {
	case "claude":
		return []string{"--effort", level}
	}
	return nil
}

// StripThinkingFlags removes a stored --effort flag and its value, so a restart puts back the level it was asked to start with and
// never two (the stored flags of an agent hold the flags it was launched with).
func StripThinkingFlags(flags []string) []string {
	out := make([]string, 0, len(flags))
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if f == "--effort" {
			if i+1 < len(flags) && !strings.HasPrefix(flags[i+1], "-") {
				i++ // its value; a following flag is not a value and stays (a bare --effort must not eat --dangerously-skip-permissions or the like)
			}
			continue
		}
		if strings.HasPrefix(f, "--effort=") {
			continue
		}
		out = append(out, f)
	}
	return out
}
