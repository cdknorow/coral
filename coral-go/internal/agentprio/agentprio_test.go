package agentprio

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func reset(t *testing.T) {
	t.Helper()
	state.Lock()
	state.setting, state.forced = "", ""
	state.Unlock()
	t.Cleanup(func() { state.Lock(); state.setting, state.forced = "", ""; state.Unlock() })
}

// Low by default; the setting picks; the flag or env wins over it (#3437).
func TestLevelPrecedence(t *testing.T) {
	reset(t)
	assert.Equal(t, Low, Level(), "default")
	SetSetting("normal")
	assert.Equal(t, Normal, Level())
	SetSetting("bogus")
	assert.Equal(t, Low, Level(), "an unknown setting means the default")
	SetSetting("normal")
	Setup("LOW")
	assert.Equal(t, Low, Level(), "the flag or env wins")
	assert.True(t, Forced())
	Setup("sideways") // ignored
	assert.Equal(t, Low, Level())
}
