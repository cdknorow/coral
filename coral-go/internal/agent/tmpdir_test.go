package agent

import (
	"fmt"
	"os"
	"testing"
)

// argvEchoEnv makes this test binary print each argument it got and exit (TestMain).
const argvEchoEnv = "CORAL_TEST_AGENT_ARGV_ECHO"

// TestMain gives the package's tests a temp folder of their own: launch lines write
// prompt files and agy's hooks folder (#3481) there, and it is removed afterwards
// instead of leaving them in the system temp folder.
func TestMain(m *testing.M) {
	if os.Getenv(hookEchoEnv) == "1" {
		// A copy of this test binary run as a Coral hook (TestHookCommandRunsTheToolsFolder):
		// it says which file it is, before any test flag is parsed.
		exe, _ := os.Executable()
		fmt.Print(exe)
		os.Exit(0)
	}
	if os.Getenv(argvEchoEnv) == "1" {
		// A copy of this test binary run as an agent's CLI (TestPromptArgReachesTheProgram…):
		// it prints each argument it got.
		for _, a := range os.Args[1:] {
			fmt.Printf("[%s]\n", a)
		}
		fmt.Print("--\n")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "coral-agent-test-")
	if err == nil {
		os.Setenv("TMPDIR", dir)
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
