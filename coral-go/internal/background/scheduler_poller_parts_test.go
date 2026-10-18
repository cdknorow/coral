package background

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Pieces of a job run and a git poll (#4599 second ranking, item 8: split out of launchAndWatch and PollOnce,
// behaviour unchanged).

func TestRunOutcome(t *testing.T) {
	for _, c := range []struct {
		launch, watch      error
		status, exitReason string
	}{
		{nil, nil, "completed", "agent_done"},
		{errors.New("no CLI"), nil, "failed", "no CLI"},
		{nil, context.DeadlineExceeded, "killed", "timeout"},
		{errors.New("no CLI"), context.DeadlineExceeded, "killed", "timeout"},
		{nil, context.Canceled, "completed", "agent_done"},
	} {
		status, reason := runOutcome(c.launch, c.watch)
		assert.Equal(t, []string{c.status, c.exitReason}, []string{status, reason})
	}
}

func TestSnapshotFor(t *testing.T) {
	remote := "git@example.invalid:acme/widgets.git"
	g := &gitInfo{Branch: "feature/x", CommitHash: "abc", CommitSubject: "wip", CommitTimestamp: "2026-10-18T00:00:00Z", RemoteURL: &remote, Worktree: true, BaseBranch: "main", Ahead: 2, Behind: 1, DirtyCount: 3}
	snap := snapshotFor(AgentInfo{AgentName: "a", AgentType: "claude", SessionID: "s1"}, "/repo", g, 7)
	assert.Equal(t, "s1", *snap.SessionID)
	assert.Equal(t, []any{"a", "claude", "/repo", "feature/x", "abc", "wip", "2026-10-18T00:00:00Z", &remote, 7, true, "main", 2, 1, 3},
		[]any{snap.AgentName, snap.AgentType, snap.WorkingDirectory, snap.Branch, snap.CommitHash, snap.CommitSubject, *snap.CommitTimestamp, snap.RemoteURL, snap.PRNumber, snap.Worktree, snap.BaseBranch, snap.Ahead, snap.Behind, snap.DirtyCount})
	assert.Nil(t, snapshotFor(AgentInfo{AgentName: "b"}, "/repo", g, 0).SessionID, "no session id: nil, not empty")
}
