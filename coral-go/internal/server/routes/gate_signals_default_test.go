package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3662 (gate phase 2 go-live): Claude agents follow their signals unless the Operator set
// gate_signals_claude to "false"; CORAL_GATE_MODE=screen stays the kill switch (gateFollowsSignals).
func TestGateSignalsAreOnByDefault(t *testing.T) {
	_, _, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	for _, c := range []struct {
		value string
		set   bool
		want  bool
	}{
		{"", false, true},      // never set: on
		{"true", true, true},   // on
		{"false", true, false}, // the Operator's off switch
		{"", true, true},       // cleared: the default
		{"nope", true, true},   // a value that is not an off spelling stays on
		{" Off ", true, false}, // the other spellings of off
		{"0", true, false},
		{"NO", true, false},
	} {
		if c.set {
			require.NoError(t, ss.SetSetting(ctx, GateSignalsClaudeSetting, c.value))
		}
		on := gateSignalsFromSettings(ss) // a fresh reader: no one-second cache between cases
		assert.Equal(t, c.want, on(), "%q (set %v)", c.value, c.set)
	}
}
