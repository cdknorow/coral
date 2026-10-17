package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rule 13 (#4176), by how the creator's and amenders' names were backed (#4255): named reviewers
// count only when the task was made and amended by the Operator verified (recorded "operator") or
// the orchestrator by an agent token (recorded "token"). By name alone (log mode) a script that claims
// to be the orchestrator is trusted; the report says where the two disagree.
func TestAutoReviewersTrustsByHowTheNameWasBacked(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for i, n := range []string{"Orchestrator", "Security Reviewer", "QA Engineer", "Dev"} {
		_, err := s.Subscribe(ctx, "p", n, n, fmt.Sprintf("claude-%d", i), nil, "all")
		require.NoError(t, err)
	}
	create := func(createdBy, identity string) *Task {
		task, err := s.CreateTaskWithOpts(ctx, "p", "T", "", "medium", createdBy, &CreateTaskOpts{
			Workflow: TaskWorkflow{Reviewers: []string{"Security Reviewer", "QA Engineer"}, CreatedIdentity: identity}}, "Dev")
		require.NoError(t, err)
		return task
	}
	reviewers := func(task *Task, byIdentity bool) (names []string, source string, differs bool) {
		got, err := s.GetTask(ctx, "p", task.ID)
		require.NoError(t, err)
		names, source, differs, err = s.AutoReviewers(ctx, "p", got, "Orchestrator", byIdentity)
		require.NoError(t, err)
		return names, source, differs
	}
	named := []string{"Security Reviewer", "QA Engineer"}

	// The orchestrator by its token, and the Operator verified: trusted either way, no disagreement.
	for _, c := range []struct{ by, identity string }{{"Orchestrator", "token"}, {"Operator", "operator"}} {
		task := create(c.by, c.identity)
		for _, byIdentity := range []bool{false, true} {
			names, source, differs := reviewers(task, byIdentity)
			assert.Equal(t, named, names, "%s/%s byIdentity=%v", c.by, c.identity, byIdentity)
			assert.Equal(t, ReviewersNamedInTask, source)
			assert.False(t, differs)
		}
	}

	// A script claiming to be the orchestrator or the Operator (recorded "claimed"): by name it is
	// trusted (log mode), by identity it is an agent's, and the disagreement is reported.
	for _, by := range []string{"Orchestrator", "Operator"} {
		task := create(by, "claimed")
		names, source, differs := reviewers(task, false)
		assert.Equal(t, named, names, "log mode trusts the claimed name %s", by)
		assert.Equal(t, ReviewersNamedInTask, source)
		assert.True(t, differs, "and says enforce would decide differently")
		names, _, differs = reviewers(task, true)
		assert.Empty(t, names, "enforce does not trust a name only claimed: %s", by)
		assert.True(t, differs)
	}

	// A task from before identities were recorded, or one whose record failed: not trusted under enforce.
	task := create("Orchestrator", "")
	names, _, differs := reviewers(task, true)
	assert.Empty(t, names)
	assert.True(t, differs)

	// A member of the team (a token, not the orchestrator) is not trusted either way.
	task = create("Dev", "token")
	for _, byIdentity := range []bool{false, true} {
		names, _, differs := reviewers(task, byIdentity)
		assert.Empty(t, names)
		assert.False(t, differs, "name and identity agree")
	}
}

// An amendment counts too: the orchestrator's task amended by a script that claims to be the
// orchestrator stops being trusted under enforce, and an amendment with no record is not trusted.
func TestAutoReviewersChecksEveryAmendment(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for i, n := range []string{"Orchestrator", "Security Reviewer", "Dev"} {
		_, err := s.Subscribe(ctx, "p", n, n, fmt.Sprintf("claude-%d", i), nil, "all")
		require.NoError(t, err)
	}
	task, err := s.CreateTaskWithOpts(ctx, "p", "T", "", "medium", "Orchestrator", &CreateTaskOpts{
		Workflow: TaskWorkflow{Reviewers: []string{"Security Reviewer"}, CreatedIdentity: "token"}}, "Dev")
	require.NoError(t, err)
	amend := func(identity string) *Task {
		got, err := s.GetTask(ctx, "p", task.ID)
		require.NoError(t, err)
		amended, err := s.AmendTask(ctx, "p", task.ID, "Orchestrator", got.Revision, "wording", map[string]interface{}{"body": "more detail"})
		require.NoError(t, err)
		if identity != "" {
			require.NoError(t, s.RecordAmendIdentity(ctx, "p", task.ID, amended.Revision, identity))
		}
		out, err := s.GetTask(ctx, "p", task.ID)
		require.NoError(t, err)
		return out
	}
	check := func(got *Task, byIdentity bool) []string {
		names, _, _, err := s.AutoReviewers(ctx, "p", got, "Orchestrator", byIdentity)
		require.NoError(t, err)
		return names
	}
	got := amend("token")
	assert.Equal(t, []string{"Security Reviewer"}, check(got, true), "amended by the orchestrator's token")
	got = amend("claimed")
	assert.Equal(t, []string{"Security Reviewer"}, check(got, false), "log mode: the name is the orchestrator's")
	assert.Empty(t, check(got, true), "enforce: an amendment only claimed is an agent's")
	got = amend("")
	assert.Empty(t, check(got, true), "enforce: an amendment with no record is not trusted")
}
