package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// #3655 F3: every Coral state hook Claude loads names the hook set's revision, so the server
// knows which set a running agent has.
func TestClaudeStateHooksCarryTheRevision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if ClaudeSignalsHooksRev > ClaudeHooksRev {
		t.Fatal("the current set must have the request signals")
	}
	want := "coral-hook-agentic-state --hooks-rev " + strconv.Itoa(ClaudeHooksRev)
	b, _ := json.Marshal(buildMergedSettings(nil)["hooks"])
	total := strings.Count(string(b), "coral-hook-agentic-state")
	withRev := strings.Count(string(b), want)
	if total == 0 || total != withRev {
		t.Fatalf("%d state hooks, %d with %q", total, withRev, want)
	}
}
