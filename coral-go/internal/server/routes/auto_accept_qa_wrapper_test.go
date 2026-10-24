package routes

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// QA #7096 round 2: a shell wrapper nested in a shell wrapper (bash -c "bash -c \"...\"") was read once per level before #7086; now each level is read by three readings that each go down again, so the time
// grows about 2.5 times per level (5 ms at depth 6, 2.6 s at depth 13 for a 16 KB command; a command of 260 KB, depth 17, would take minutes). Reading it must stay in bounded time, or call it risky.
func TestShellRiskNestedShellWrappersAreBoundedInTime(t *testing.T) {
	wd := t.TempDir()
	dq := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`)
	cmd := "echo hi"
	for d := 0; d < 13; d++ {
		cmd = "bash -c \"" + dq.Replace(cmd) + "\""
	}
	start := time.Now()
	_ = shellRisk(cmd, wd)
	assert.Less(t, time.Since(start), 1*time.Second, "bash -c nested 13 deep (%d bytes) took %s", len(cmd), time.Since(start))
}
