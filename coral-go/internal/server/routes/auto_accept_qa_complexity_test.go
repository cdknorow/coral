package routes

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// QA #7096: reading a command must stay cheap however it is nested. 4000 nested $( ) (32 KB) took 6 s with the parser reading and 2 ms before it; the cost grows with the square of the depth (the
// cap on what is parsed is 256 KB, where it took 3.5 minutes). A command this nested is either read in bounded time or called risky (it waits for the person): never a stall.
func TestShellRiskNestedSubstitutionIsBoundedInTime(t *testing.T) {
	wd := t.TempDir()
	cmd := strings.Repeat("echo $(", 4000) + "echo hi" + strings.Repeat(")", 4000)
	start := time.Now()
	_ = shellRisk(cmd, wd)
	assert.Less(t, time.Since(start), 3*time.Second, "4000 nested $( ) took %s", time.Since(start))
}
