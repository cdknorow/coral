//go:build windows

package startup

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
)

// A restart is not held up by an agent that is still running: a program Coral started does not inherit its listening socket, so
// when the server closes the port (it ends, or a relaunch replaces it) the next Coral binds it at once, exclusively, with the old
// agent's terminal still open. Were the handle inherited, the port would stay taken until that agent ended and the new Coral
// would fail with "in use?".
func TestAnAgentStartedByCoralDoesNotKeepItsPortAfterItCloses(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	first, err := listenTCP("127.0.0.1:0")
	require.NoError(t, err)
	port := strconv.Itoa(first.Addr().(*net.TCPAddr).Port)

	b := ptymanager.NewPTYBackend()
	name := "claude-6077-held-port"
	require.NoError(t, b.Spawn(name, "claude", t.TempDir(), "6077", "powershell.exe -NoProfile", 120, 40))
	t.Cleanup(func() { _ = b.Kill(name) })
	time.Sleep(2 * time.Second) // the shell is up and holds whatever it inherited

	require.True(t, b.IsRunning(name), "the agent's terminal is still running while the port is closed and re-opened")
	first.Close()
	again, err := listenTCP("127.0.0.1:" + port)
	require.NoError(t, err, "Coral binds its port again at once although an agent it started is still running")
	again.Close()
	require.True(t, b.IsRunning(name), "the agent was running through the whole restart")
}
