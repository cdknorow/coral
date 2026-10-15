package cloud

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// #3544: the hub indicator says "connected since": ConnectedAt is set when the tunnel comes
// up, kept while it stays up (a repeated "connected" doesn't move it), and cleared otherwise.
func TestConnectedAt(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	st := Status{State: StateConnecting}
	applyTunnelState(&st, StateConnected, "", t0)
	if !st.ConnectedAt.Equal(t0) {
		t.Fatalf("connected: ConnectedAt = %v, want %v", st.ConnectedAt, t0)
	}
	applyTunnelState(&st, StateConnected, "", t0.Add(time.Minute))
	if !st.ConnectedAt.Equal(t0) {
		t.Errorf("still connected: ConnectedAt moved to %v", st.ConnectedAt)
	}
	applyTunnelState(&st, StateRetrying, "the hub is unreachable; retrying", t0.Add(2*time.Minute))
	if !st.ConnectedAt.IsZero() || st.LastError == "" {
		t.Errorf("retrying: ConnectedAt = %v (want zero), LastError %q", st.ConnectedAt, st.LastError)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "connected_at") {
		t.Errorf("not connected: the JSON has connected_at: %s", b)
	}
	applyTunnelState(&st, StateConnected, "", t0.Add(3*time.Minute))
	b, _ = json.Marshal(st)
	if !strings.Contains(string(b), `"connected_at":"2026-10-15T07:03:00Z"`) {
		t.Errorf("reconnected: %s", b)
	}
}
