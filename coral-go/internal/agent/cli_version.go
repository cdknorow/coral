package agent

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/executil"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// cliVersionRe finds a version in a CLI's --version output: "2.1.300 (Claude Code)",
// "codex-cli 0.155.1", "1.3.5".
var cliVersionRe = regexp.MustCompile(`\d+\.\d+\.\d+[0-9A-Za-z.+-]*`)

// VersionNumber is the version in a CLI's --version output ("2.1.300 (Claude Code)" gives "2.1.300"), or "" when there is none.
func VersionNumber(output string) string { return cliVersionRe.FindString(output) }

// cliVersions caches each CLI path's version with the binary's modification time and size,
// so an update of the CLI is noticed and an unchanged one is asked only once. One entry per
// path: an update replaces it, so the cache doesn't grow with every version ever seen (#4057).
var cliVersions sync.Map // path -> cliVersion

type cliVersion struct {
	stamp   string // modification time and size
	version string
}

// CLIVersion returns the version agentType's CLI reports (its --version), found the
// way launch finds it (FindCLI), or "" when the CLI or its version is unknown
// (#3481, #3475: the state model records which CLI version each session runs, so a
// CLI update that changes its signals can be told apart).
func CLIVersion(settings map[string]string, agentType string) string {
	if GetCLIInfo(agentType) == nil {
		return ""
	}
	path, err := FindCLI(settings, agentType)
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stamp := fmt.Sprintf("%d|%d", info.ModTime().UnixNano(), info.Size())
	if v, ok := cliVersions.Load(path); ok && v.(cliVersion).stamp == stamp {
		return v.(cliVersion).version
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = CLIEnv(agentenv.Filter(os.Environ()), path) // the server's own CORAL_* settings never reach a CLI; an npm-installed one runs with its own node (#5899)
	// A CLI that is a wrapper (node, npm shims) can leave a child holding stdout past
	// the kill: stop waiting for the pipes 2 s after it (Backend's review).
	cmd.WaitDelay = 2 * time.Second
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
	executil.HideWindow(cmd)  // Windows: a console program started by a Coral with no console of its own would open a visible window (#211); its output still comes through the pipes
	out, _ := cmd.Output()
	v := cliVersionRe.FindString(string(out))
	if ctx.Err() == nil {
		cliVersions.Store(path, cliVersion{stamp: stamp, version: v})
	}
	return v
}
