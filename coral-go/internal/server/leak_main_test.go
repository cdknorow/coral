package server

import (
	"os"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167).
func TestMain(m *testing.M) {
	code := m.Run()
	ptymanager.StopGate(10 * time.Second) // the input gate's retry loop, asleep after a held send
	os.Exit(testleak.Check(code))
}
