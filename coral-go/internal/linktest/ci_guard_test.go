package linktest

import (
	"os"
	"testing"
)

// Symlink and RequireSymlinks fail rather than skip only when CI is set: on GitHub Actions (which
// sets both CI and GITHUB_ACTIONS on every job) a job without CI would quietly skip every link
// check, so this fails it instead (Security's PR #53 review).
func TestCIIsSetWhereGitHubActionsRuns(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "" && os.Getenv("CI") == "" {
		t.Fatal("GITHUB_ACTIONS is set but CI is not: the symlink tests would skip instead of failing; set CI=true for this job")
	}
}
