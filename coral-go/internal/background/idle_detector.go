package background

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

const needsInputThreshold = 300 // 5 minutes

// IdleDetector detects agents waiting for input and creates webhook deliveries.
type IdleDetector struct {
	taskStore    *store.TaskStore
	webhookStore *store.WebhookStore
	sessionStore *store.SessionStore
	interval     time.Duration
	logger       *slog.Logger
	discoverFn   func(ctx context.Context) ([]AgentInfo, error)
	notifiedMu   sync.Mutex
	notified     map[string]bool // Track which agents we've already notified
}

// NewIdleDetector creates a new IdleDetector.
func NewIdleDetector(taskStore *store.TaskStore, webhookStore *store.WebhookStore, interval time.Duration) *IdleDetector {
	return &IdleDetector{
		taskStore:    taskStore,
		webhookStore: webhookStore,
		interval:     interval,
		logger:       slog.Default().With("service", "idle_detector"),
		notified:     make(map[string]bool),
	}
}

// SetSessionStore sets the session store for sleep-awareness.
func (d *IdleDetector) SetSessionStore(ss *store.SessionStore) {
	d.sessionStore = ss
}

// SetDiscoverFn sets a custom agent discovery function.
func (d *IdleDetector) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	d.discoverFn = fn
}

// Run starts the detection loop.
func (d *IdleDetector) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := d.RunOnce(ctx); err != nil {
				d.logger.Error("detection error", "error", err)
			}
		}
	}
}

// RunOnce performs a single idle detection pass.
func (d *IdleDetector) RunOnce(ctx context.Context) error {
	configs, err := d.webhookStore.ListWebhookConfigs(ctx, true)
	if err != nil || len(configs) == 0 {
		return err
	}

	agents, err := d.discoverFn(ctx)
	if err != nil {
		return err
	}

	// Build set of sleeping session IDs to skip
	sleepingSIDs := make(map[string]bool)
	if d.sessionStore != nil {
		allLive, err := d.sessionStore.GetAllLiveSessions(ctx)
		if err == nil {
			for _, ls := range allLive {
				if ls.IsSleeping == 1 {
					sleepingSIDs[ls.SessionID] = true
				}
			}
		}
	}

	sessionIDs := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.SessionID != "" {
			sessionIDs = append(sessionIDs, a.SessionID)
		}
	}
	stateEvents, err := d.taskStore.GetSessionStateEvents(ctx, sessionIDs)
	if err != nil {
		return err
	}

	for _, agent := range agents {
		// Skip sleeping sessions — they are intentionally idle
		if sleepingSIDs[agent.SessionID] {
			d.notifiedMu.Lock()
			delete(d.notified, agent.AgentName)
			d.notifiedMu.Unlock()
			continue
		}
		// Only a request made directly to the user counts. An agent that
		// finished its turn and received the idle reminder is not waiting
		// on anyone and must not page a webhook.
		waiting := needsInput(stateEvents[agent.SessionID])

		if !waiting {
			d.notifiedMu.Lock()
			delete(d.notified, agent.AgentName)
			d.notifiedMu.Unlock()
			continue
		}

		// Check log file staleness
		logPath := fmt.Sprintf("%s/%s_coral_%s.log", os.TempDir(), agent.AgentType, agent.SessionID)
		info, err := os.Stat(logPath)
		if err != nil {
			continue
		}
		staleness := time.Since(info.ModTime()).Seconds()
		if staleness < needsInputThreshold {
			continue
		}

		d.notifiedMu.Lock()
		alreadyNotified := d.notified[agent.AgentName]
		d.notifiedMu.Unlock()
		if alreadyNotified {
			continue
		}

		for _, cfg := range configs {
			if cfg.AgentFilter != nil && *cfg.AgentFilter != "" && *cfg.AgentFilter != agent.AgentName {
				continue
			}
			minutes := int(staleness / 60)
			d.webhookStore.CreateWebhookDelivery(ctx,
				cfg.ID, agent.AgentName, "needs_input",
				fmt.Sprintf("Agent needs input — waiting for %d minutes", minutes),
				&agent.SessionID)
		}
		d.notifiedMu.Lock()
		d.notified[agent.AgentName] = true
		d.notifiedMu.Unlock()
	}

	return nil
}

// needsInput reports whether a session is blocked on a request made directly
// to the user, using the same derivation as the dashboard.
func needsInput(events []store.AgentEvent) bool {
	in := sessionstate.Input{}
	for _, ev := range events {
		in.Events = append(in.Events, sessionstate.Event{Type: ev.EventType, Summary: ev.Summary})
	}
	return sessionstate.Derive(in).NeedsInput
}
