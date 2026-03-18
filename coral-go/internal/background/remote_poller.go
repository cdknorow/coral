package background

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
)

// RemoteSubscription represents a subscription to a remote board.
type RemoteSubscription struct {
	ID                 int64  `db:"id" json:"id"`
	SessionID          string `db:"session_id" json:"session_id"`
	RemoteServer       string `db:"remote_server" json:"remote_server"`
	Project            string `db:"project" json:"project"`
	LastNotifiedUnread int    `db:"last_notified_unread" json:"last_notified_unread"`
}

// RemoteBoardStore is the interface for remote board subscription storage.
type RemoteBoardStore interface {
	ListAll(ctx context.Context) ([]RemoteSubscription, error)
	UpdateLastNotified(ctx context.Context, id int64, unread int) error
}

// RemoteBoardPoller polls remote Coral servers for unread board messages.
type RemoteBoardPoller struct {
	store      RemoteBoardStore
	tmux       *tmux.Client
	client     *http.Client
	interval   time.Duration
	logger     *slog.Logger
	discoverFn func(ctx context.Context) ([]AgentInfo, error)
}

// NewRemoteBoardPoller creates a new RemoteBoardPoller.
func NewRemoteBoardPoller(store RemoteBoardStore, tmuxClient *tmux.Client, interval time.Duration) *RemoteBoardPoller {
	return &RemoteBoardPoller{
		store:    store,
		tmux:     tmuxClient,
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
	subs, err := p.store.ListAll(ctx)
	if err != nil || len(subs) == 0 {
		return err
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
			tmuxName := fmt.Sprintf("%s-%s", agent.AgentType, agent.SessionID)
			agentMap[tmuxName] = agent
		}
	}

	for _, sub := range subs {
		agent, ok := agentMap[sub.SessionID]
		if !ok {
			continue
		}

		// Check unread on remote server
		url := fmt.Sprintf("%s/api/board/%s/messages/check?session_id=%s",
			sub.RemoteServer, sub.Project, sub.SessionID)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
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
		json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()

		if data.Unread == 0 {
			if sub.LastNotifiedUnread != 0 {
				p.store.UpdateLastNotified(ctx, sub.ID, 0)
			}
			continue
		}

		if sub.LastNotifiedUnread == data.Unread {
			continue
		}

		// Send nudge
		plural := "s"
		if data.Unread == 1 {
			plural = ""
		}
		nudge := fmt.Sprintf("You have %d unread message%s on the message board. Run 'coral-board read' to see them.",
			data.Unread, plural)
		err = p.tmux.SendKeys(ctx, agent.AgentName, nudge, agent.AgentType, agent.SessionID)
		if err != nil {
			continue
		}

		p.store.UpdateLastNotified(ctx, sub.ID, data.Unread)
	}

	return nil
}
