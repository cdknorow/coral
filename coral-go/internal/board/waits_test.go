package board

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisteredWaitsLifecycle(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	// Register wait for Orchestrator
	wait, err := s.RegisterWait(ctx, "proj", "Music SFX director", "sess-1", "message", "Orchestrator", "waiting for design revision", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "active", wait.Status)
	assert.Equal(t, "Orchestrator", wait.TargetID)

	// GetActiveWait returns it
	active, err := s.GetActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, wait.ID, active.ID)

	// Registering a second wait supersedes and cancels the previous one
	wait2, err := s.RegisterWait(ctx, "proj", "Music SFX director", "sess-1", "task", "876", "waiting for task 876", time.Hour)
	require.NoError(t, err)
	assert.NotEqual(t, wait.ID, wait2.ID)

	active2, err := s.GetActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)
	require.NotNil(t, active2)
	assert.Equal(t, wait2.ID, active2.ID)
	assert.Equal(t, "task", active2.WaitType)

	// Cancel wait
	err = s.CancelActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)

	activeAfterCancel, err := s.GetActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)
	assert.Nil(t, activeAfterCancel)
}

func TestResolveMatchingMessageWaits(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	// Subscribe agent
	_, err = s.Subscribe(ctx, "proj", "Music SFX director", "Music", "sess-music", nil, "mentions")
	require.NoError(t, err)

	// Register wait for Orchestrator
	_, err = s.RegisterWait(ctx, "proj", "Music SFX director", "sess-music", "message", "Orchestrator", "waiting for review", time.Hour)
	require.NoError(t, err)

	// Unrelated message from Lead Developer does not resolve
	matched, err := s.ResolveMatchingMessageWaits(ctx, "proj", "Lead Developer", "Unrelated post about UI")
	require.NoError(t, err)
	assert.Empty(t, matched)

	// Message from Orchestrator resolves!
	matched, err = s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "Candidate accepted")
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "Music SFX director", matched[0].SubscriberID)
	assert.Equal(t, "resolved", matched[0].Status)

	// Active wait is now gone
	active, err := s.GetActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)
	assert.Nil(t, active)
}

func TestResolveMatchingMessageWaits_ExplicitMention(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	_, err = s.Subscribe(ctx, "proj", "Music SFX director", "Music", "sess-music", nil, "mentions")
	require.NoError(t, err)

	// Wait specifically for Lead Developer
	_, err = s.RegisterWait(ctx, "proj", "Music SFX director", "sess-music", "message", "Lead Developer", "waiting for audio engine", time.Hour)
	require.NoError(t, err)

	// Game Design Director posts but explicitly mentions @Music SFX director
	matched, err := s.ResolveMatchingMessageWaits(ctx, "proj", "Game Design Director", "@Music SFX director here is the new audio brief")
	require.NoError(t, err)
	require.Len(t, matched, 1, "direct mention should resolve wait even if sender differs from target")
}

func TestResolveMatchingMessageWaits_IgnoresMessageAddressedToAnotherAgent(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	_, err = s.Subscribe(ctx, "proj", "SEO Agent", "SEO Agent", "sess-seo", nil, "mentions")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "proj", "Lead Developer", "Lead Developer", "sess-lead", nil, "mentions")
	require.NoError(t, err)
	_, err = s.RegisterWait(ctx, "proj", "SEO Agent", "sess-seo", "message", "Orchestrator", "waiting for next instruction", time.Hour)
	require.NoError(t, err)

	matched, err := s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "@Lead Developer Candidate #987 c98e626/tree 6106dbcc fixes")
	require.NoError(t, err)
	assert.Empty(t, matched, "a message explicitly addressed to another agent must not wake SEO")
	active, err := s.GetActiveWait(ctx, "proj", "SEO Agent")
	require.NoError(t, err)
	require.NotNil(t, active)
}

func TestResolveMatchingTaskWaits(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	// Register wait for task 876
	_, err = s.RegisterWait(ctx, "proj", "Music SFX director", "sess-music", "task", "876", "waiting for campaign design", time.Hour)
	require.NoError(t, err)

	// Unrelated task 870 does not match
	matched, err := s.ResolveMatchingTaskWaits(ctx, "proj", 870)
	require.NoError(t, err)
	assert.Empty(t, matched)

	// Task 876 matches
	matched, err = s.ResolveMatchingTaskWaits(ctx, "proj", 876)
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "resolved", matched[0].Status)
}

func TestResolveMatchingCommitWaits(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	// Register wait for commit 45cc813d
	_, err = s.RegisterWait(ctx, "proj", "Music SFX director", "sess-music", "commit", "45cc813d", "waiting for landing", time.Hour)
	require.NoError(t, err)

	// Unrelated commit does not match
	matched, err := s.ResolveMatchingCommitWaits(ctx, "proj", "abcdef123456")
	require.NoError(t, err)
	assert.Empty(t, matched)

	// Matching commit (even full 40-char SHA) matches
	matched, err = s.ResolveMatchingCommitWaits(ctx, "proj", "45cc813d9876543210fedcba")
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "resolved", matched[0].Status)
}

func TestRegisteredWait_Expiration(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()

	// Register wait that expires in 10 milliseconds
	wait, err := s.RegisterWait(ctx, "proj", "Music SFX director", "sess-music", "message", "Orchestrator", "waiting", 10*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, wait)

	time.Sleep(25 * time.Millisecond)

	active, err := s.GetActiveWait(ctx, "proj", "Music SFX director")
	require.NoError(t, err)
	assert.Nil(t, active, "expired wait should return nil from GetActiveWait")
}

// A question for the Operator (#4175) is listed until the Operator's own message, and no
// agent's message, even one that tags the asker, ends it.
func TestOperatorQuestionEndsOnlyWithTheOperator(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	_, err = s.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "sess-orch", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "proj", "Dev", "Developer", "sess-dev", nil, "all")
	require.NoError(t, err)

	_, err = s.RegisterWait(ctx, "proj", "Orchestrator", "sess-orch", WaitOperator, "Operator", "ship v1.4.72 tonight?", time.Hour)
	require.NoError(t, err)
	qs, err := s.ListOperatorQuestions(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, qs, 1)
	assert.Equal(t, "ship v1.4.72 tonight?", qs[0].Reason)

	// An agent's message does not answer it, even when it tags the asker or says "Operator".
	matched, err := s.ResolveMatchingMessageWaits(ctx, "proj", "Dev", "@Orchestrator the Operator says yes")
	require.NoError(t, err)
	assert.Empty(t, matched)
	qs, _ = s.ListOperatorQuestions(ctx, "proj")
	assert.Len(t, qs, 1)

	matched, err = s.ResolveMatchingMessageWaits(ctx, "proj", "Operator", "yes, ship it")
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "Orchestrator", matched[0].SubscriberID)
	qs, _ = s.ListOperatorQuestions(ctx, "proj")
	assert.Empty(t, qs)
}

// #247: a task or commit wait also ends on a message that tags the waiter by name; a message that doesn't (or tags someone
// else) leaves it waiting.
func TestAnExplicitMentionEndsATaskOrCommitWait(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreWithKey(filepath.Join(tmp, "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for _, who := range []string{"Codex Tester", "QA Engineer", "Lead Developer"} {
		_, err = s.Subscribe(ctx, "proj", who, who, "sess-"+who, nil, "mentions")
		require.NoError(t, err)
	}
	_, err = s.RegisterWait(ctx, "proj", "Codex Tester", "sess-Codex Tester", "task", "223", "waiting for #223", time.Hour)
	require.NoError(t, err)
	_, err = s.RegisterWait(ctx, "proj", "QA Engineer", "sess-QA Engineer", "commit", "abc1234", "waiting for the merge", time.Hour)
	require.NoError(t, err)

	matched, err := s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "status update for everyone")
	require.NoError(t, err)
	assert.Empty(t, matched, "a message for no one in particular leaves both waiting")
	matched, err = s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "@Lead Developer please look")
	require.NoError(t, err)
	assert.Empty(t, matched, "a message for someone else leaves both waiting")

	matched, err = s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "@Codex Tester please stop and read the board")
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "Codex Tester", matched[0].SubscriberID)
	assert.Equal(t, "resolved", matched[0].Status)
	active, err := s.GetActiveWait(ctx, "proj", "QA Engineer")
	require.NoError(t, err)
	require.NotNil(t, active, "the commit wait of someone not named still waits")

	matched, err = s.ResolveMatchingMessageWaits(ctx, "proj", "Orchestrator", "@QA Engineer the merge moved")
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "commit", matched[0].WaitType)
}
