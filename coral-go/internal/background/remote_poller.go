package background

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// RemoteBoardPoller polls remote Coral servers for unread board messages.
type RemoteBoardPoller struct {
	store      *store.RemoteBoardStore
	runtime    AgentRuntime
	client     *http.Client
	interval   time.Duration
	logger     *slog.Logger
	discoverFn func(ctx context.Context) ([]AgentInfo, error)
}

// NewRemoteBoardPoller creates a new RemoteBoardPoller.
func NewRemoteBoardPoller(rbStore *store.RemoteBoardStore, runtime AgentRuntime, interval time.Duration) *RemoteBoardPoller {
	return &RemoteBoardPoller{
		store:    rbStore,
		runtime:  runtime,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: interval,
		logger:   slog.Default().With("service", "remote_board_poller"),
	}
}

// SetDiscoverFn sets a custom agent discovery function.
func (p *RemoteBoardPoller) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	p.discoverFn = fn
}

// Run starts the polling loop.
func (p *RemoteBoardPoller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.RunOnce(ctx); err != nil {
				p.logger.Error("poll error", "error", err)
			}
		}
	}
}

// RunOnce performs a single remote poll pass.
func (p *RemoteBoardPoller) RunOnce(ctx context.Context) error {
	subs, err := p.store.ListAllRemoteSubs(ctx)
	if err != nil || len(subs) == 0 {
		return err
	}

	if p.discoverFn == nil {
		return nil
	}
	agents, err := p.discoverFn(ctx)
	if err != nil {
		return err
	}

	// Build agent map for nudging
	agentMap := make(map[string]AgentInfo)
	for _, agent := range agents {
		if agent.SessionID != "" {
			agentMap[agent.SessionID] = agent
			tmuxName := naming.SessionName(agent.AgentType, agent.SessionID)
			agentMap[tmuxName] = agent
		}
	}

	for _, sub := range subs {
		agent, ok := agentMap[sub.SessionID]
		if !ok {
			continue
		}

		// SSRF protection: validate remote server URL doesn't resolve to internal IPs
		if _, err := httputil.ResolveAndValidateURL(sub.RemoteServer); err != nil {
			p.logger.Warn("remote board subscription blocked by SSRF check",
				"server", sub.RemoteServer, "session_id", sub.SessionID, "error", err)
			continue
		}

		// Check unread on remote server
		url := fmt.Sprintf("%s/api/board/%s/messages/check?session_id=%s",
			sub.RemoteServer, sub.Project, sub.SessionID)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			p.logger.Warn("failed to create remote poll request", "server", sub.RemoteServer, "error", err)
			continue
		}
		resp, err := p.client.Do(req)
		if err != nil {
			p.logger.Debug("remote poll failed", "server", sub.RemoteServer, "error", err)
			continue
		}
		var data struct {
			Unread int `json:"unread"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
			p.logger.Warn("failed to decode remote poll response", "server", sub.RemoteServer, "error", err)
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if data.Unread == 0 {
			if sub.LastNotifiedUnread != 0 {
				p.store.UpdateLastNotified(ctx, sub.ID, 0)
			}
			continue
		}

		// One nudge until the agent reads (unread drops to 0); more messages
		// arriving meanwhile would otherwise stack up nudges in its input.
		if sub.LastNotifiedUnread != 0 {
			continue
		}

		// Send nudge
		plural := "s"
		if data.Unread == 1 {
			plural = ""
		}
		nudge := fmt.Sprintf("You have %d unread message%s on the message board. Run 'coral-board read' to see them.",
			data.Unread, plural)
		sessionName := naming.SessionName(agent.AgentType, agent.SessionID)
		err = p.runtime.SendInput(ctx, sessionName, nudge)
		if err != nil {
			continue
		}

		p.store.UpdateLastNotified(ctx, sub.ID, data.Unread)
	}

	return nil
}
