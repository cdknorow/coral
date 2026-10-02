package main

import (
	"strings"
	"testing"
)

func TestTaskUsageCoversCurrentCapabilities(t *testing.T) {
	usage := taskUsageText()
	for _, capability := range []string{
		"claim [id]", "current", "detail <id>", "edit <id>", "publish <id>",
		"complete <id>", "cancel <id>", "--blocked-by", "--completion-gates",
		"--candidate-revision", "immutable",
	} {
		if !strings.Contains(usage, capability) {
			t.Errorf("task usage missing %q", capability)
		}
	}
}
