package background

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/tmux"
)

// BoardNotifier nudges agents when they have unread board messages.
type BoardNotifier struct {
	boardStore *board.Store
	tmux       *tmux.Client
	interval   time.Duration
	logger     *slog.Logger
	discoverFn func(ctx context.Context) ([]AgentInfo, error)
	notified   map[string]int // session_id -> unread count at last notification
}

// NewBoardNotifier creates a new BoardNotifier.
func NewBoardNotifier(boardStore *board.Store, tmuxClient *tmux.Client, interval time.Duration) *BoardNotifier {
	return &BoardNotifier{
		boardStore: boardStore,
		tmux:       tmuxClient,
		interval:   interval,
		logger:     slog.Default().With("service", "board_notifier"),
		notified:   make(map[string]int),
	}
}

// SetDiscoverFn sets a custom agent discovery function.
func (n *BoardNotifier) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	n.discoverFn = fn
}

// Run starts the notification loop.
func (n *BoardNotifier) Run(ctx context.Context) error {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := n.RunOnce(ctx); err != nil {
				n.logger.Error("notification error", "error", err)
			}
		}
	}
}

// RunOnce performs a single notification pass.
func (n *BoardNotifier) RunOnce(ctx context.Context) error {
	agents, err := n.discoverFn(ctx)
	if err != nil {
		return err
	}

	liveBoardIDs := make(map[string]bool)

	for _, agent := range agents {
		if agent.SessionID == "" {
			continue
		}

		// Board uses tmux session name as subscriber ID: {type}-{uuid}
		boardSID := fmt.Sprintf("%s-%s", agent.AgentType, agent.SessionID)
		liveBoardIDs[boardSID] = true

		sub, err := n.boardStore.GetSubscription(ctx, boardSID)
		if err != nil || sub == nil {
			continue
		}
		// Skip remote subscribers
		if sub.OriginServer != nil && *sub.OriginServer != "" {
			continue
		}

		unread, err := n.boardStore.CheckUnread(ctx, sub.Project, boardSID)
		if err != nil {
			continue
		}

		if unread == 0 {
			delete(n.notified, boardSID)
			continue
		}

		if n.notified[boardSID] == unread {
			continue
		}

		// Send nudge
		plural := "s"
		if unread == 1 {
			plural = ""
		}
		nudge := fmt.Sprintf("You have %d unread message%s on the message board. Run 'coral-board read' to see them.", unread, plural)
		err = n.tmux.SendKeys(ctx, agent.AgentName, nudge, agent.AgentType, agent.SessionID)
		if err != nil {
			n.logger.Debug("failed to nudge agent", "agent", agent.AgentName, "error", err)
			continue
		}

		n.notified[boardSID] = unread
	}

	// Clean up stale entries
	for sid := range n.notified {
		if !liveBoardIDs[sid] {
			delete(n.notified, sid)
		}
	}

	return nil
}
