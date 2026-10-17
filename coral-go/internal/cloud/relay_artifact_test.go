package cloud

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// RelayArtifact (cross-team artifacts, C2): without a live tunnel it is ErrNotLinked, and a transfer
// waiting for the one slot gives up with its context.
func TestRelayArtifactWithoutATunnelIsNotLinked(t *testing.T) {
	m := &Manager{artifactSlots: make(chan struct{}, 1)}
	if _, err := m.RelayArtifact(context.Background(), 4, bytes.NewReader([]byte("abcd"))); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayArtifactSlotIsOneTransferAtATime(t *testing.T) {
	m := &Manager{artifactSlots: make(chan struct{}, 1)}
	m.session.Store(&tunnel.ClientSession{}) // never reached: the slot is taken
	m.artifactSlots <- struct{}{}            // a transfer is running
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.RelayArtifact(ctx, 4, bytes.NewReader([]byte("abcd"))); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second transfer should wait for the slot and give up with its context, got %v", err)
	}
}
