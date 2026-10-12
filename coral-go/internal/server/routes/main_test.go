package routes

import (
	"os"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
)

func TestMain(m *testing.M) {
	// The terminal tests drive shells, which need no 300ms between a command
	// and its Enter (production keeps it for agent TUIs).
	tmux.SetSendKeysSettleForTest(20 * time.Millisecond)
	os.Exit(m.Run())
}
