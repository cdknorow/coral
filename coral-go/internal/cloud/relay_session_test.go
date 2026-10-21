package cloud

import (
	"context"
	"testing"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #3489: a session reported by a tunnel generation that was stopped is never stored, so a
// stopped (or switched-off) link answers ErrNotLinked, not a stale closed session.
func TestRelaySessionFromAStoppedTunnelIsIgnored(t *testing.T) {
	m := New(Options{})
	m.mu.Lock()
	gen := m.gen
	m.mu.Unlock()

	m.setSession(gen, &tunnel.ClientSession{})
	if m.session.Load() == nil {
		t.Fatal("the current generation's session is stored")
	}

	m.mu.Lock()
	m.stopLocked()
	m.mu.Unlock()
	if m.session.Load() != nil {
		t.Fatal("a stop clears the session")
	}
	m.setSession(gen, &tunnel.ClientSession{}) // the old tunnel reports late
	if m.session.Load() != nil {
		t.Fatal("a stopped generation's session is not stored")
	}
	if _, err := m.Relay(context.Background(), tunnel.RelayMessagePath, []byte(`{}`)); err != ErrNotLinked {
		t.Fatalf("Relay after stop: %v, want ErrNotLinked", err)
	}
}
