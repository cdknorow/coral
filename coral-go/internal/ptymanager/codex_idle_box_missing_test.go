package ptymanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6466 (#6447's soak): a real Codex that had finished its turn (its Stop signal seen, nothing open) showed its idle composer without the marker the gate looks for
// ("  Ask Codex to do anything", "  ? for shortcuts"), the gate took the missing box for a Codex mid-turn ("never pasted blind into Codex", #247), held the reminder, asked for a
// redraw every half minute and replaced the held send each interval: nothing was typed for 25 minutes while the agent sat idle. The agent's own signals come first: idle by them is not mid-turn.

// codexNoMarker is Codex's idle screen as the soak's final capture shows it: the placeholder with no "›", a lone footer.
const codexNoMarker = "• Probe 4 posted to @Soak Beta.\n\n  Worked for 5s • 5:15 AM\n\n\n  Ask Codex to do anything\n\n\n  ? for shortcuts\n"

func codexPaste(term *stuckTerm, name, text string) AutomatedInput {
	in := pasteSend(term, name, text, "board message", false)
	in.AgentType = "codex"
	return in
}

func idleByStop() AgentState {
	return AgentState{State: StateIdle, Since: time.Now().Add(-90 * time.Second), LastEvent: time.Now().Add(-90 * time.Second), LastStop: time.Now().Add(-90 * time.Second)}
}

func TestAnIdleCodexWhoseBoxIsNotOnTheScreenStillGetsTheSend(t *testing.T) {
	stuckGate(t, time.Hour, time.Hour)
	withState(t, idleByStop(), true)
	name := "codex-idle-no-marker"
	term := &stuckTerm{screen: codexNoMarker, echo: true}
	t.Cleanup(func() { DropHeld(name, "test done") })

	d, err := DeliverAutomated(context.Background(), codexPaste(term, name, "[Coral reminder] post one pulse."))
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "the signals say idle: the missing box is not a reason to hold")
	pasted, enters := term.got()
	assert.Equal(t, []string{"[Coral reminder] post one pulse."}, pasted)
	assert.Equal(t, 1, enters)
	assert.Equal(t, 0, HeldCount(name))
}

// The other way round stays as it was (#247): mid-turn by the signals, or no signals at all, a Codex whose box is not on screen is not pasted into.
func TestACodexMidTurnOrWithoutSignalsIsStillNotPastedBlind(t *testing.T) {
	for name, tc := range map[string]struct {
		st AgentState
		ok bool
	}{
		"in turn":               {AgentState{State: StateInTurn, Since: time.Now()}, true},
		"idle but no Stop seen": {AgentState{State: StateIdle}, true},
		"no signals":            {AgentState{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			stuckGate(t, time.Hour, time.Hour)
			withState(t, tc.st, tc.ok)
			sess := "codex-blind-" + name
			term := &stuckTerm{screen: codexNoMarker, echo: true}
			t.Cleanup(func() { DropHeld(sess, "test done") })
			d, err := DeliverAutomated(context.Background(), codexPaste(term, sess, "[Coral reminder] post one pulse."))
			require.NoError(t, err)
			assert.Equal(t, Held, d)
			pasted, _ := term.got()
			assert.Empty(t, pasted, "nothing was pasted")
		})
	}
}

// A paste that went into a composer the screen cannot read back is held first (its Enter withheld); on its turn again, a Codex that has stopped gets its Enter, once, and the queue is free.
func TestAnIdleCodexWhosePasteCannotBeReadBackIsEnteredOnItsTurn(t *testing.T) {
	stuckGate(t, time.Hour, time.Hour)
	withState(t, idleByStop(), true)
	name := "codex-idle-readback"
	term := &stuckTerm{screen: codexNoMarker, echo: false}
	t.Cleanup(func() { DropHeld(name, "test done") })
	in := codexPaste(term, name, "[Coral reminder] post one pulse.")
	// the first look finds no box: the paste goes (signals first); its verification finds none either, so its Enter is withheld and the send is held
	_, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, e := term.got(); return e == 1 }, 3*time.Second, 10*time.Millisecond, "on its turn again the Enter goes")
	require.Eventually(t, func() bool { return HeldCount(name) == 0 }, 3*time.Second, 10*time.Millisecond, "nothing stays held")
	pasted, enters := term.got()
	assert.Len(t, pasted, 1, "pasted once, never again")
	assert.Equal(t, 1, enters)
}
