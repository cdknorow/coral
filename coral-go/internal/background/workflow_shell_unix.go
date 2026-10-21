//go:build !windows

package background

// workflowShell is the shell workflow steps and hooks run their command with.
func workflowShell() (string, error) { return "sh", nil }
