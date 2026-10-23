package routes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6219 review: a choice that replaces a pending one is typed after the earlier one, and Claude Code writes the earlier command's output when it gets to it, which can be after the newer
// switch has started waiting. That late entry is the OLD command's answer, not the new one's: reading it as the new one's failed the new switch ("set the level to low, not high") although Claude
// Code then took the new level (Codex Test, #6214: two requests 1 ms apart, the second failed).
func TestAReplacedSwitchsLateOutputIsNotTheNewSwitchsAnswer(t *testing.T) {
	for _, userFormat := range []bool{false, true} {
		name := "system entries"
		if userFormat {
			name = "user entries (2.1.309)"
		}
		t.Run(name, func(t *testing.T) {
			r := newSwitchRig(t, "claude")
			r.userFormat = userFormat
			code, m := r.post(t, map[string]any{"thinking": "low"})
			require.Equal(t, http.StatusAccepted, code, m)
			r.waitTyped(t, "/effort low")
			code, m = r.post(t, map[string]any{"thinking": "high"})
			require.Equal(t, http.StatusAccepted, code, m)
			r.waitTyped(t, "/effort high")
			// Claude Code gets to the first command now: its entries are written after the second switch started, then the second command's
			r.localCommand(t, "/effort", "low", "Set effort level to low (saved as your default for new sessions)")
			r.localCommand(t, "/effort", "high", "Set effort level to high (saved as your default for new sessions)")
			sw := r.waitState(t, switchDone)
			assert.Equal(t, switchDone, sw.State, "the late answer to the replaced command is not this switch's: %+v", sw)
			_, th := r.storedModel(t)
			assert.Equal(t, "high", th)
		})
	}
}

// The same for a model: the replaced switch's /model output is not the new one's answer.
func TestAReplacedModelSwitchsLateOutputIsNotTheNewSwitchsAnswer(t *testing.T) {
	r := newSwitchRig(t, "claude")
	code, m := r.post(t, map[string]any{"model": "claude-opus-4-1"})
	require.Equal(t, http.StatusAccepted, code, m)
	r.waitTyped(t, "/model claude-opus-4-1")
	code, m = r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
	require.Equal(t, http.StatusAccepted, code, m)
	r.waitTyped(t, "/model claude-sonnet-5-5")
	r.localCommand(t, "/model", "claude-opus-4-1", "Set model to claude-opus-4-1")
	r.localCommand(t, "/model", "claude-sonnet-5-5", "Set model to claude-sonnet-5-5")
	sw := r.waitState(t, switchDone)
	assert.Equal(t, switchDone, sw.State, "%+v", sw)
	model, _ := r.storedModel(t)
	assert.Equal(t, "claude-sonnet-5-5", model)
}
