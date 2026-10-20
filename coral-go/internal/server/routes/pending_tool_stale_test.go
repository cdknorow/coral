package routes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #5254: a late PostToolUse of an earlier tool does not end the wait of the tool that is pending now.
func TestStaleToolUseEventKeepsThePendingToolWaiting(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000005256"
	h := &SessionsHandler{}
	h.pending.set(sid, pendingTool{ToolUseID: "tu-B", ToolName: "Bash", At: time.Now()})
	h.notePendingToolEvent(sid, "permission_request", "")
	h.notePendingToolEvent(sid, "tool_use", "tu-A") // the PostToolUse of the call before
	assert.True(t, h.hints.mayBeOpen(sid), "tool B still waits")
	_, has := h.pending.get(sid, time.Now())
	assert.True(t, has)
	h.notePendingToolEvent(sid, "tool_use", "tu-B") // its own
	assert.False(t, h.hints.mayBeOpen(sid))
	_, has = h.pending.get(sid, time.Now())
	assert.False(t, has)
}
