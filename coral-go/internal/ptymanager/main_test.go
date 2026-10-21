package ptymanager

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/testleak"
	"github.com/subgentic/coral/internal/tmux"
)

// envEchoEnv makes this test binary print its environment and exit (TestMain). Not a CORAL_
// name: the agent's environment filter keeps it.
const envEchoEnv = "PTYMANAGER_TEST_ENV_ECHO"

// argvEchoEnv makes a copy of this test binary, run as an agent's CLI, print each argument
// it got and exit (TestCodexLaunchScriptPassesItsArgumentsThroughWindowsPowerShell).
const argvEchoEnv = "PTYMANAGER_TEST_ARGV_ECHO" // not a CORAL_ name: the agent environment filter would drop it

// TestMain removes the tmux variables a developer's shell may carry. Running these tests from inside
// a Coral (or any tmux) session would otherwise point the tests' tmux clients at that live server.
func TestMain(m *testing.M) {
	if os.Getenv(envEchoEnv) == "1" {
		// A copy of this test binary run as an agent (TestWindowsAgentGetsTheFilteredEnvironment):
		// it prints its environment, one variable per line, and exits.
		for _, kv := range os.Environ() {
			fmt.Printf("ENV:%s\n", kv)
		}
		fmt.Print("ENV-END\n")
		os.Exit(0)
	}
	if os.Getenv(argvEchoEnv) == "1" {
		for _, a := range os.Args[1:] {
			fmt.Printf("ARG[%s]END\n", a)
		}
		os.Exit(0)
	}
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	// The tests' tmux servers start their panes with $SHELL. A developer's interactive shell and its
	// rc files make typed commands wait on an unknown startup; a plain sh has none.
	os.Setenv("SHELL", "/bin/sh")
	// The tests drive shells, which need no 300ms between a command and its
	// Enter (production keeps it for agent TUIs).
	tmux.SetSendKeysSettleForTest(20 * time.Millisecond)
	code := m.Run()
	StopGate(10 * time.Second)    // the gate's retry loop, asleep after a held send
	os.Exit(testleak.Check(code)) // no goroutine left running (#4167)
}
