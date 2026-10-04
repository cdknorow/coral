package board

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTeamArtifactTasksIsBoundedInSQLAndScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		task, err := s.CreateTask(ctx, "team", fmt.Sprintf("t%d", i), "", "medium", "lead")
		require.NoError(t, err)
		_, err = s.CompleteTaskWithArtifacts(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "a", Content: "x"}})
		require.NoError(t, err)
	}
	plain, err := s.CreateTask(ctx, "team", "plain", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", plain.ID, "lead", nil, "success", nil)
	require.NoError(t, err)
	other, err := s.CreateTask(ctx, "other", "o", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "other", other.ID, "lead", nil, "success", []TaskArtifact{{Name: "o", Content: "x"}})
	require.NoError(t, err)

	tasks, truncated, err := s.ListTeamArtifactTasks(ctx, "team", 2)
	require.NoError(t, err)
	require.True(t, truncated, "older matching tasks exist beyond the scan limit")
	require.Len(t, tasks, 2, "only limit tasks are decoded")
	require.Greater(t, tasks[0].TaskID, tasks[1].TaskID, "newest task first")

	all, truncated, err := s.ListTeamArtifactTasks(ctx, "team", 100)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, all, 4, "tasks without artifacts and other teams' tasks are excluded")
}

func subscribeAgent(t *testing.T, s *Store, project, subscriber, sessionUUID string) {
	t.Helper()
	_, err := s.Subscribe(context.Background(), project, subscriber, subscriber, "claude-"+sessionUUID, nil, nil, "all")
	require.NoError(t, err)
}

func completeAs(t *testing.T, s *Store, project, actor, title string, arts []TaskArtifact) int64 {
	t.Helper()
	task, err := s.CreateTask(context.Background(), project, title, "", "medium", actor)
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(context.Background(), project, task.ID, actor, nil, "success", arts)
	require.NoError(t, err)
	return task.ID
}

// The session filter must narrow the bounded SQL scan itself: another agent's
// newer tasks may not crowd the selected agent's tasks out of the window.
func TestListTeamArtifactTasksQueryFiltersBeforeTheScanLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	subscribeAgent(t, s, "team", "Alice", "aaaaaaaa-0000")
	subscribeAgent(t, s, "team", "Bob", "bbbbbbbb-0000")
	a1 := completeAs(t, s, "team", "Alice", "a1", []TaskArtifact{{Name: "a1", Content: "x"}})
	a2 := completeAs(t, s, "team", "Alice", "a2", []TaskArtifact{{Name: "a2", Content: "x"}})
	for i := 0; i < 4; i++ {
		completeAs(t, s, "team", "Bob", fmt.Sprintf("b%d", i), []TaskArtifact{{Name: "b", Content: "x"}})
	}
	subs, err := s.TeamSubscriberSessions(ctx, "team")
	require.NoError(t, err)
	require.Equal(t, "aaaaaaaa-0000", subs["Alice"])

	// Unfiltered with a window of 2 only sees Bob's newest tasks.
	recent, truncated, err := s.ListTeamArtifactTasksQuery(ctx, TeamArtifactQuery{Project: "team", Limit: 2})
	require.NoError(t, err)
	require.True(t, truncated)
	for _, task := range recent {
		require.Equal(t, "Bob", task.CompletedBy)
	}

	// Filtered with the same window of 2 still returns both of Alice's.
	mine, truncated, err := s.ListTeamArtifactTasksQuery(ctx, TeamArtifactQuery{Project: "team", Limit: 2, SessionID: "aaaaaaaa-0000", Subscribers: subs})
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, mine, 2)
	require.Equal(t, a2, mine[0].TaskID)
	require.Equal(t, a1, mine[1].TaskID)

	// Producer attribution comes from stored ownership only.
	producer, session := ArtifactProducer(mine[0], "completion", subs)
	require.Equal(t, "Alice", producer)
	require.Equal(t, "aaaaaaaa-0000", session)
	// A completed_by with no subscriber mapping is unresolved, never guessed.
	producer, session = ArtifactProducer(TeamArtifactTask{CompletedBy: "Operator"}, "completion", subs)
	require.Equal(t, "Operator", producer)
	require.Empty(t, session)
	// No completer recorded at all.
	producer, session = ArtifactProducer(TeamArtifactTask{}, "completion", subs)
	require.Empty(t, producer)
	require.Empty(t, session)
}

// The claim-time session is only trusted for the claimant. After a
// reassignment, or when a third party completes or reviews, attribution must
// follow the actual producer's own mapping, never the stored task session.
func TestArtifactProducerGuardsClaimantSession(t *testing.T) {
	subs := map[string]string{"Alice": "sess-alice", "Bob": "sess-bob", "Carol": "sess-carol"}
	task := TeamArtifactTask{AssignedTo: "Alice", SessionID: "sess-alice-at-claim"}

	// Claimant completed: the recorded claim session wins over the current mapping.
	task.CompletedBy = "Alice"
	producer, session := ArtifactProducer(task, "completion", subs)
	if producer != "Alice" || session != "sess-alice-at-claim" {
		t.Fatalf("claimant completion = %q/%q", producer, session)
	}

	// Reassigned/third-party completion: Bob's own mapping, not Alice's claim session.
	task.CompletedBy = "Bob"
	producer, session = ArtifactProducer(task, "completion", subs)
	if producer != "Bob" || session != "sess-bob" {
		t.Fatalf("third-party completion = %q/%q", producer, session)
	}

	// Third-party review submission (not the claimant).
	task.Review = &CompletionReview{SubmittedBy: "Carol"}
	producer, session = ArtifactProducer(task, "review", subs)
	if producer != "Carol" || session != "sess-carol" {
		t.Fatalf("third-party review = %q/%q", producer, session)
	}

	// Third party with no subscriber mapping stays unresolved, even though the
	// task has a recorded session for someone else.
	task.Review = &CompletionReview{SubmittedBy: "Operator"}
	producer, session = ArtifactProducer(task, "review", subs)
	if producer != "Operator" || session != "" {
		t.Fatalf("unmapped third-party review = %q/%q", producer, session)
	}

	// Claimant's review uses the claim session.
	task.Review = &CompletionReview{SubmittedBy: "Alice"}
	if _, session = ArtifactProducer(task, "review", subs); session != "sess-alice-at-claim" {
		t.Fatalf("claimant review session = %q", session)
	}
}
