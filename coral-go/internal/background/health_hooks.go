package background

import (
	"context"
	"time"
)

// AgentSignal is an agent's state from its hook events, as the health rules read it.
type AgentSignal = agentSignal

// HealthHooks replace how the health rules see the clock, the agents, their state and the gate,
// for a test that drives the rules from outside this package (the end-to-end health scenario,
// #4246). A nil field leaves that part as it is.
//
// Test hooks; not called outside tests. They are exported only because the scenario lives in
// another package; TestSetHooksIsCalledOnlyFromTests fails if a non-test file calls SetHooks.
type HealthHooks struct {
	Now         func() time.Time
	Discover    func(ctx context.Context) ([]AgentInfo, error)
	Signal      func(ctx context.Context, sessionID string) (AgentSignal, bool)
	Deliver     func(ctx context.Context, a AgentInfo, key, text string) error
	HeldDialogs func() map[string]time.Time
	HeldDrafts  func() map[string]time.Time
}

// SetHooks replaces the nudge rules' clock, agents, state, gate and held dialogs.
func (h *HealthNudges) SetHooks(k HealthHooks) {
	if k.Now != nil {
		h.now = k.Now
	}
	if k.Discover != nil {
		h.discover = k.Discover
	}
	if k.Signal != nil {
		h.signal = k.Signal
	}
	if k.Deliver != nil {
		h.deliver = k.Deliver
	}
	if k.HeldDialogs != nil {
		h.heldDialogs = k.HeldDialogs
	}
	if k.HeldDrafts != nil {
		h.heldDrafts = k.HeldDrafts
	}
}

// SetHooks replaces the check's clock, agents, state and gate.
func (m *BoardHealthMonitor) SetHooks(k HealthHooks) {
	if k.Now != nil {
		m.now = k.Now
	}
	if k.Discover != nil {
		m.discover = k.Discover
	}
	if k.Signal != nil {
		m.signal = k.Signal
	}
	if k.Deliver != nil {
		m.deliver = k.Deliver
	}
}

// SetHooks replaces the reminders' clock, agents and gate.
func (t *TaskReminders) SetHooks(k HealthHooks) {
	if k.Now != nil {
		t.now = k.Now
	}
	if k.Discover != nil {
		t.discover = k.Discover
	}
	if k.Deliver != nil {
		t.deliver = k.Deliver
	}
}

// Scan runs the check once for every board (the scenario steps it; Run calls it on its ticker).
func (m *BoardHealthMonitor) Scan(ctx context.Context) { m.scan(ctx) }
