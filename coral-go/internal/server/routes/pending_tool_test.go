package routes

import (
	"testing"
	"time"
)

func TestPendingToolsDurationMs(t *testing.T) {
	started := time.Date(2026, 9, 21, 23, 0, 0, 0, time.UTC)
	p := pendingTools{}
	p.set("session-1", pendingTool{ToolUseID: "call-1", ToolName: "Bash", At: started})

	ms, ok := p.durationMs("session-1", "call-1", started.Add(2350*time.Millisecond))
	if !ok || ms != 2350 {
		t.Fatalf("duration = %d, %v; want 2350, true", ms, ok)
	}
	if _, ok := p.durationMs("session-1", "different-call", started.Add(time.Second)); ok {
		t.Fatal("mismatched tool call received a duration")
	}
	if _, ok := p.durationMs("session-1", "call-1", started.Add(pendingToolTTL+time.Second)); ok {
		t.Fatal("stale tool call received a duration")
	}
}

func TestPendingToolsDurationClampsFastCalls(t *testing.T) {
	started := time.Now()
	p := pendingTools{}
	p.set("session-1", pendingTool{At: started})
	if ms, ok := p.durationMs("session-1", "", started); !ok || ms != 1 {
		t.Fatalf("duration = %d, %v; want 1, true", ms, ok)
	}
}
