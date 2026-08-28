package routes

import (
	"testing"

	"github.com/cdknorow/coral/internal/store"
)

func runsWithStatuses(statuses ...string) []store.ScheduledRun {
	runs := make([]store.ScheduledRun, 0, len(statuses))
	for _, st := range statuses {
		runs = append(runs, store.ScheduledRun{Status: st})
	}
	return runs
}

// A job whose scheduling works but whose every run fails presents as healthy:
// enabled, with a next fire time, and failures only visible inside the run
// history nobody opens. This count is what makes it visible.
func TestCountConsecutiveFailedRuns(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		want     int
	}{
		{"no runs yet", nil, 0},
		{"healthy job", []string{"completed", "completed"}, 0},
		{"failing every run", []string{"failed", "failed", "failed"}, 3},
		{"recovered after failures", []string{"completed", "failed", "failed"}, 0},
		{"failing again after a success", []string{"failed", "completed", "failed"}, 1},
		// A run that is currently trying is not yet a run that failed again,
		// so it stops the count rather than being counted.
		{"currently retrying", []string{"running", "failed", "failed"}, 0},
		{"pending stops the count", []string{"pending", "failed"}, 0},
		{"killed is not counted as failed", []string{"killed", "failed"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := countConsecutiveFailedRuns(runsWithStatuses(tc.statuses...))
			if got != tc.want {
				t.Fatalf("countConsecutiveFailedRuns(%v) = %d, want %d", tc.statuses, got, tc.want)
			}
		})
	}
}
