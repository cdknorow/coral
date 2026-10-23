package routes

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
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

// The confirm waits are the operator's own bug: 30 minutes held every other choice. They are 30 s and the next reply is not what they wait for.
func TestSwitchConfirmDefaultsAreThirtySeconds(t *testing.T) {
	tm := switchTiming.Load()
	assert.Equal(t, 30*time.Second, tm.levelConfirm)
	assert.Equal(t, 30*time.Second, tm.modelConfirm)
}

// Two switches of one agent never type at once: a choice waits for the run lock, and types nothing while another run holds it.
func TestASwitchTypesNothingWhileAnotherRunHoldsTheLock(t *testing.T) {
	r := newSwitchRig(t, "claude")
	lock := r.h.modelSwitches.runLock(r.sid)
	lock.Lock()
	code, m := r.post(t, map[string]any{"thinking": "low"})
	require.Equal(t, http.StatusAccepted, code, m)
	time.Sleep(400 * time.Millisecond)
	assert.Empty(t, r.typed(), "nothing is typed while another run holds the lock")
	lock.Unlock()
	r.waitTyped(t, "/effort low")
	assert.Same(t, lock, r.h.modelSwitches.runLock(r.sid), "one lock per agent")
	assert.NotSame(t, lock, r.h.modelSwitches.runLock(r.sid+"-other"), "and not shared between agents")
}

// A wake starts at the stored model and level, each once: the launch's own --model and --effort (either spelling) are replaced, never doubled.
func TestWakeFlagsReplaceTheLaunchModelAndLevelOnce(t *testing.T) {
	for _, launched := range [][]string{
		{"--model", "claude-opus-4-1", "--effort", "high", "--verbose"},
		{"-m", "claude-opus-4-1", "--effort", "high", "--verbose"},
		{"--model=claude-opus-4-1", "--effort", "high", "--verbose"},
	} {
		model, level := "claude-sonnet-5-5", "low"
		flags := wakeFlags(&store.LiveSession{AgentType: "claude", Model: &model, Thinking: &level, Flags: store.MarshalFlags(launched)})
		models, efforts := 0, 0
		for i, f := range flags {
			switch {
			case f == "--model" || f == "-m" || strings.HasPrefix(f, "--model="):
				models++
				require.Less(t, i+1, len(flags))
				assert.Equal(t, model, flags[i+1], "%v", flags)
			case f == "--effort":
				efforts++
				assert.Equal(t, level, flags[i+1], "%v", flags)
			}
		}
		assert.Equal(t, 1, models, "%v -> %v", launched, flags)
		assert.Equal(t, 1, efforts, "%v -> %v", launched, flags)
		assert.Contains(t, flags, "--verbose", "the other flags stay")
	}
}
