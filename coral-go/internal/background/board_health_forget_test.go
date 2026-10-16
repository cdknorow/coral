package background

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 5): the monitor's memory of what it reported does not grow with every task ever seen.
func TestBoardHealthForgetsOldReports(t *testing.T) {
	m := NewBoardHealthMonitor(nil, time.Minute)
	now := time.Unix(1_800_000_000, 0)
	m.idleNotified[1] = now.Add(-boardHealthForgetAfter - time.Hour)
	m.idleNotified[2] = now.Add(-time.Hour)
	m.last["escalated:1"] = now.Add(-boardHealthForgetAfter - time.Hour).UTC().Format(time.RFC3339)
	m.last["escalated:2"] = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	m.lastIssue["kept"] = "an open issue"
	m.lastIssue["resolved"] = ""
	m.lastIssue["deleted-board"] = "text of a board that is gone"

	m.forgetOld(now, map[string]bool{"kept": true, "resolved": true})

	assert.NotContains(t, m.idleNotified, int64(1))
	assert.Contains(t, m.idleNotified, int64(2))
	assert.NotContains(t, m.last, "escalated:1")
	assert.Contains(t, m.last, "escalated:2")
	assert.Equal(t, map[string]string{"kept": "an open issue"}, m.lastIssue)
}
