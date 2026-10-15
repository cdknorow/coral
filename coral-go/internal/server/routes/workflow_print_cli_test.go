package routes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3522: a non-interactive ("print") agent step runs only on a CLI verified for it (Claude
// for now); another CLI is refused at save with a clear message, an interactive step on it
// is allowed, and an unknown agent_type is refused.
func TestValidateStepsPrintStepCLIs(t *testing.T) {
	step := func(agent string, interactive bool) []json.RawMessage {
		b, _ := json.Marshal(map[string]any{"name": "s", "type": "agent", "prompt": "do it", "interactive": interactive,
			"agent": map[string]string{"agent_type": agent}})
		return []json.RawMessage{b}
	}
	assert.Equal(t, "", validateSteps(step("claude", false), nil), "claude print steps are supported")
	for _, cli := range []string{"codex", "gemini", "agy", "antigravity", "pi"} {
		assert.Contains(t, validateSteps(step(cli, false), nil), "aren't supported for "+cli+" yet", cli)
		assert.Equal(t, "", validateSteps(step(cli, true), nil), "%s: an interactive step is allowed", cli)
	}
	assert.Contains(t, validateSteps(step("foo", true), nil), `unknown agent_type "foo"`)

	// The workflow's default agent decides when the step names none.
	noAgent, _ := json.Marshal(map[string]any{"name": "s", "type": "agent", "prompt": "do it"})
	assert.Contains(t, validateSteps([]json.RawMessage{noAgent}, json.RawMessage(`{"agent_type":"codex"}`)), "aren't supported for codex yet")
	assert.Equal(t, "", validateSteps([]json.RawMessage{noAgent}, json.RawMessage(`{"agent_type":"claude"}`)))
}
