package board

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// #3170: blocked reasons are read by people (task detail) and agents: plain words, not status enums.
func TestDependencyReasonsAreInPlainWords(t *testing.T) {
	for _, c := range []struct {
		status, outcome, condition, want string
	}{
		{"in_progress", "", "success", "not finished yet (in progress); this task starts when it succeeds"},
		{"pending", "", "failure", "not finished yet (not started); this task starts when it fails"},
		{"review_pending", "", "termination", "not finished yet (in review); this task starts when it finishes"},
		{"skipped", "", "success", "it was cancelled, but this task needs it to succeed"},
		{"completed", "failed", "success", "it failed, but this task needs it to succeed"},
		{"completed", "", "failure", "it succeeded, but this task needs it to fail"},
	} {
		_, _, reason := dependencyStatus(c.status, TaskWorkflow{Outcome: c.outcome}, c.condition, nil)
		require.Equal(t, c.want, reason, "%s/%s/%s", c.status, c.outcome, c.condition)
	}
	_, _, met := dependencyStatus("completed", TaskWorkflow{}, "success", nil)
	require.Empty(t, met, "a met dependency has no reason")
}
