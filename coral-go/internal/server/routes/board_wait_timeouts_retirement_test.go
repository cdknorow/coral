package routes

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
	"testing"
	"time"
)

type waitTimeoutFaultTerminal struct {
	*mockSessionTerminal
	sendErr error
}

func (t *waitTimeoutFaultTerminal) SendInput(ctx context.Context, o terminalinput.Origin, name, text, agentType, sessionID string) error {
	if t.sendErr != nil {
		return t.sendErr
	}
	return t.mockSessionTerminal.SendInput(ctx, o, name, text, agentType, sessionID)
}

func TestWaitTimeoutPermanentRefusalsRetireButTransientErrorsRetry(t *testing.T) {
	for _, kind := range []string{"live-identity-gone", "terminal-gone", "temporary-lookup", "temporary-send"} {
		t.Run(kind, func(t *testing.T) {
			_, h := setupBoardTestServer(t)
			ctx := context.Background()
			const name = "codex-71050003-0000-4000-8000-000000000001"
			term := &waitTimeoutFaultTerminal{mockSessionTerminal: newMockTerminal()}
			term.addSession(name, "/tmp/timeout-refusal")
			h.SetTerminal(term)
			_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", name, nil, "mentions")
			require.NoError(t, err)
			_, err = h.bs.RegisterWait(ctx, "team", "Worker", name, "task", "42", "", time.Nanosecond)
			require.NoError(t, err)
			switch kind {
			case "live-identity-gone":
				h.SetLiveSessionLookup(func(context.Context, string) (string, bool, error) { return "", false, nil })
			case "terminal-gone":
				term.sendErr = ptymanager.ErrTerminalGone
			case "temporary-lookup":
				h.SetLiveSessionLookup(func(context.Context, string) (string, bool, error) {
					return "", false, errors.New("lookup unavailable")
				})
			case "temporary-send":
				term.sendErr = errors.New("transport unavailable")
			}
			h.dispatchWaitTimeouts(ctx)
			require.Empty(t, term.sentTo(name))
			if kind == "live-identity-gone" || kind == "terminal-gone" {
				require.NoError(t, h.bs.DispatchExpiredWaitNotices(ctx, func(board.RegisteredWait) error { t.Error("permanently refused timeout still pending"); return nil }))
				return
			}
			// A transient failure must survive the retirement classification and recover.
			h.SetLiveSessionLookup(func(context.Context, string) (string, bool, error) { return "codex", true, nil })
			term.sendErr = nil
			h.dispatchWaitTimeouts(ctx)
			require.Len(t, term.sentTo(name), 1)
			h.dispatchWaitTimeouts(ctx)
			require.Len(t, term.sentTo(name), 1)
		})
	}
}
