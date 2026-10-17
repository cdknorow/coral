package teamhealth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var checkNow = time.Date(2026, 10, 17, 3, 40, 0, 0, time.UTC)

// With nothing to judge the check is one line, and it is sent either way (it says plainly when
// nothing moved).
func TestComposeAllClear(t *testing.T) {
	out := Compose(CheckInput{Now: checkNow, Agents: 6, Progress: Progress{Since: checkNow.Add(-10 * time.Minute), Completed: 3, Posts: 12}})
	assert.Equal(t, "[Coral health check] All 6 agents progressing; 3 tasks completed, 12 posts since 03:30.", out)
	out = Compose(CheckInput{Now: checkNow, Agents: 6, Progress: Progress{Since: checkNow.Add(-10 * time.Minute)}})
	assert.Equal(t, "[Coral health check] Nothing needs you; no progress since the last check (03:30).", out)
}

// Items come ranked, each with its evidence, the agent's quoted words and a suggestion; then the
// operator's questions, then the team line.
func TestComposeRanksItems(t *testing.T) {
	out := Compose(CheckInput{
		Now: checkNow, Agents: 5,
		Items: []Item{
			{Rank: 2, Agent: "QA", Problem: "idle 25 min with nothing assigned", Suggest: "assign it the next task"},
			{Rank: 1, Agent: "Dev", Problem: "silent on #12 for 34 min since Coral asked for a status", Said: ShownText("@Orchestrator approve", nil), Suggest: "check on it, or reassign #12"},
		},
		Questions: []Question{{Asker: "Orchestrator", Text: ShownText("ship v1.4.72 tonight?", nil), Since: checkNow.Add(-50 * time.Minute)}},
		Progress:  Progress{Since: checkNow.Add(-10 * time.Minute)},
	})
	assert.True(t, strings.HasPrefix(out, "[Coral health check] 2 need you: 1) Dev: silent on #12"), out)
	assert.Contains(t, out, `Last said, quoted: (at)Orchestrator approve`)
	assert.Contains(t, out, "2) QA: idle 25 min")
	assert.Contains(t, out, "Waiting on the operator: Orchestrator asked 50 min ago: quoted: ship v1.4.72 tonight?")
	assert.True(t, strings.HasSuffix(out, "No progress since the last check (03:30)."), out)
	assert.NotContains(t, out, "\n", "one line: typed as one paste")
}

// Case C4's "check size <= 4096": however many items, the check fits, keeping the highest-ranked
// items and the operator's questions and counting what it left out.
func TestComposeStaysWithinTheCap(t *testing.T) {
	var items []Item
	for i := 0; i < 200; i++ {
		items = append(items, Item{Rank: i, Agent: fmt.Sprintf("Agent %d", i), Problem: "idle " + strings.Repeat("x", 60), Suggest: "assign it work"})
	}
	out := Compose(CheckInput{Now: checkNow, Agents: 200, Items: items,
		Questions: []Question{{Asker: "Orchestrator", Text: ShownText("deploy?", nil), Since: checkNow}},
		Progress:  Progress{Since: checkNow}})
	assert.LessOrEqual(t, len(out), MaxCheck)
	assert.Contains(t, out, "1) Agent 0:")
	assert.Contains(t, out, "more not shown.")
	assert.Contains(t, out, "Waiting on the operator")
}
