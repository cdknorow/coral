package routes

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6225 review: Claude took the command but wrote its transcript entry after landWait. The command is then typed once more (the retype cannot tell a slow write from a dropped command), which is
// the known cost: at most two copies, never a third, and the switch still ends done on the first entry, with the second copy's answer ("Kept model as ...") never turning it into a failure.
func TestASlowTranscriptWriteTypesAtMostTwoCopiesAndStillConfirms(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		setSwitchTiming(t, switchTimings{poll: 5 * time.Millisecond, idleWait: 400 * time.Millisecond, modelConfirm: 2 * time.Second, levelConfirm: 2 * time.Second, settle: 10 * time.Millisecond, landWait: 100 * time.Millisecond})
		code, m := r.post(t, map[string]any{"model": "claude-sonnet-5-5"})
		require.Equal(t, http.StatusAccepted, code, m)
		require.Eventually(t, func() bool { return len(r.typed()) == 2 }, 3*time.Second, 5*time.Millisecond, "past landWait the command is typed again")
		r.localCommand(t, "/model", "claude-sonnet-5-5", "Set model to claude-sonnet-5-5")
		r.localCommand(t, "/model", "claude-sonnet-5-5", "Kept model as claude-sonnet-5-5")
		sw := r.waitState(t, switchDone)
		assert.Equal(t, switchDone, sw.State, "%+v", sw)
		time.Sleep(150 * time.Millisecond) // sleep-ok: no third copy
		assert.Len(t, r.typed(), 2)
		model, _ := r.storedModel(t)
		assert.Equal(t, "claude-sonnet-5-5", model)
	})
	t.Run("level", func(t *testing.T) {
		r := newSwitchRig(t, "claude")
		setSwitchTiming(t, switchTimings{poll: 5 * time.Millisecond, idleWait: 400 * time.Millisecond, modelConfirm: 2 * time.Second, levelConfirm: 2 * time.Second, settle: 10 * time.Millisecond, landWait: 100 * time.Millisecond})
		code, m := r.post(t, map[string]any{"thinking": "low"})
		require.Equal(t, http.StatusAccepted, code, m)
		require.Eventually(t, func() bool { return len(r.typed()) == 2 }, 3*time.Second, 5*time.Millisecond)
		r.localCommand(t, "/effort", "low", "Set effort level to low (saved as your default for new sessions)")
		r.localCommand(t, "/effort", "low", "Set effort level to low (saved as your default for new sessions)")
		sw := r.waitState(t, switchDone)
		assert.Equal(t, switchDone, sw.State, "%+v", sw)
		_, th := r.storedModel(t)
		assert.Equal(t, "low", th)
		assert.Len(t, r.typed(), 2)
	})
}
