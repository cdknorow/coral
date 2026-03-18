package background

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

const needsInputThreshold = 300 // 5 minutes

// IdleDetector detects agents waiting for input and creates webhook deliveries.
type IdleDetector struct {
	taskStore    *store.TaskStore
	webhookStore *store.WebhookStore
	interval     time.Duration
	logger       *slog.Logger
	discoverFn   func(ctx context.Context) ([]AgentInfo, error)
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

	sessionIDs := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.SessionID != "" {
			sessionIDs = append(sessionIDs, a.SessionID)
		}
	}
	latestEvents, err := d.taskStore.GetLatestEventTypes(ctx, sessionIDs)
	if err != nil {
		return err
	}

	for _, agent := range agents {
		evPair, ok := latestEvents[agent.SessionID]
		latestEv := ""
		if ok {
			latestEv = evPair[0]
		}
		waiting := latestEv == "stop" || latestEv == "notification"

		if !waiting {
			delete(d.notified, agent.AgentName)
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

		if d.notified[agent.AgentName] {
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
		d.notified[agent.AgentName] = true
	}

	return nil
}
