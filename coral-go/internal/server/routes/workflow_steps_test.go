package routes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func rawSteps(t *testing.T, steps ...map[string]any) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(steps))
	for _, s := range steps {
		b, _ := json.Marshal(s)
		out = append(out, b)
	}
	return out
}

// #3423 CRIT-02: output_artifact is a relative path inside the step's folder.
func TestValidateStepsOutputArtifact(t *testing.T) {
	step := func(a string) map[string]any {
		return map[string]any{"name": "s", "type": "shell", "command": "echo x", "output_artifact": a}
	}
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", "..", "a\x00b"} {
		assert.Contains(t, validateSteps(rawSteps(t, step(bad)), nil), "output_artifact", bad)
	}
	for _, ok := range []string{"out.txt", "reports/out.txt", "a/./b.txt"} {
		assert.Empty(t, validateSteps(rawSteps(t, step(ok)), nil), ok)
	}
}

// #3423 CRIT-01: a content template already quoted in the command gets a warning (the
// runner inserts it as one quoted word); unquoted and agent prompts don't.
func TestStepWarningsForQuotedContentTemplates(t *testing.T) {
	steps := rawSteps(t,
		map[string]any{"name": "a", "type": "shell", "command": "echo {{prev_stdout_content}}"},
		map[string]any{"name": "b", "type": "shell", "command": `echo "{{prev_stdout_content}}"`},
		map[string]any{"name": "c", "type": "shell", "command": "echo '{{step_0_stdout_content}}' and \\'{{prev_artifact_content}}"},
		map[string]any{"name": "d", "type": "agent", "prompt": `review "{{prev_stdout_content}}"`},
	)
	w := stepWarnings(steps)
	assert.Len(t, w, 2, w)
	assert.True(t, strings.Contains(w[0], "step 1 (b)") && strings.Contains(w[1], "step 2 (c)"), w)
	assert.Contains(t, w[1], "{{step_0_stdout_content}}")

	out := withWarnings(map[string]any{"id": 1}, w).(map[string]any)
	assert.Equal(t, w, out["warnings"])
	assert.Equal(t, map[string]any{"id": 1}, withWarnings(map[string]any{"id": 1}, nil))
}
