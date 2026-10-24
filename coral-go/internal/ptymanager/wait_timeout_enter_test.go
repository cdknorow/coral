package ptymanager

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHeldEnterRechecksOriginalNoticeBeforeSubmitting(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-current", true: "cancelled"}[cancelled], func(t *testing.T) {
			// Local queue with its worker disabled: inspect the queued Enter directly,
			// without retry-loop timing or global gate configuration.
			g := &inputGate{held: make(map[string][]*heldInput), running: true}
			e := &enterAgent{fakeAgent: &fakeAgent{screen: idleInputBox}}
			const name = "claude-timeout-held-enter"
			in := AutomatedInput{Term: e, Name: name, Key: "wait-timeout:1", Text: "timeout", Ready: func(context.Context) error {
				if cancelled {
					return ErrNotWanted
				}
				return nil
			}}
			g.holdEnter(in, kindPrompt)
			require.Len(t, g.held[name], 1)
			err := g.held[name][0].in.Send(context.Background())
			if cancelled {
				require.ErrorIs(t, err, ErrNotWanted)
				require.Empty(t, e.sent())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"<Enter>"}, e.sent())
			}
		})
	}
}
