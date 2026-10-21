//go:build !windows

package routes

import "testing"

// promptReceived is the prompt a launched CLI gets from a prompt file: outside Windows the file
// holds the prompt itself.
func promptReceived(t *testing.T, content string) string { return content }
