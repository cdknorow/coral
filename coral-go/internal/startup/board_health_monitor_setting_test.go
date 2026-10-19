package startup

import "testing"

// The dashboard's Settings saves board_health_monitor as a JSON boolean, which is stored as "False" or "false"; the service starts unless it is false (#5128).
func TestBoardHealthMonitorEnabled(t *testing.T) {
	for value, want := range map[string]bool{"": true, "true": true, "True": true, "garbage": true, "false": false, "False": false, " FALSE ": false} {
		if got := boardHealthMonitorEnabled(map[string]string{"board_health_monitor": value}); got != want {
			t.Errorf("board_health_monitor %q: enabled = %v, want %v", value, got, want)
		}
	}
	if !boardHealthMonitorEnabled(nil) {
		t.Error("no settings: the health service is on by default")
	}
}
