package routes

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
)

// QA #7118 round 2: a timeout notice for an agent whose CLI has exited can never be typed. It must be given up on (receipted or dropped), not retried every second for ever with a warning each time.
func TestAWaitTimeoutNoticeForAnExitedAgentIsNotRetriedForEver(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	const name = "codex-00000002-3864-4864-8864-000000000002"
	term := newFGTerminal(newMockTerminal())
	term.addSession(name, "/tmp/exited-fixture")
	term.setFG(name, "zsh") // the CLI has exited: its terminal is an idle shell
	h.SetTerminal(term)
	_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", name, nil, "mentions")
	require.NoError(t, err)
	_, err = h.bs.RegisterWait(ctx, "team", "Worker", name, "task", "42", "", time.Nanosecond)
	require.NoError(t, err)

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })

	for i := 0; i < 5; i++ { // five passes, as the 1 s ticker would run them
		h.dispatchWaitTimeouts(ctx)
	}
	assert.Empty(t, term.sentTo(name), "nothing is typed into an exited agent's shell")
	warns := strings.Count(logs.String(), "dispatch board wait timeouts")
	assert.LessOrEqual(t, warns, 1, "five passes logged %d warnings for the same undeliverable notice", warns)
	stillPending := 0
	require.NoError(t, h.bs.DispatchExpiredWaitNotices(ctx, func(board.RegisteredWait) error { stillPending++; return nil }))
	assert.Zero(t, stillPending, "the notice for an exited agent is still pending after five passes")
}
