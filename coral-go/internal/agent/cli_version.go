package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// cliVersionRe finds a version in a CLI's --version output: "2.1.300 (Claude Code)",
// "codex-cli 0.155.1", "1.3.5".
var cliVersionRe = regexp.MustCompile(`\d+\.\d+\.\d+[0-9A-Za-z.+-]*`)

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
	// A CLI that is a wrapper (node, npm shims) can leave a child holding stdout past
	// the kill: stop waiting for the pipes 2 s after it (Backend's review).
	cmd.WaitDelay = 2 * time.Second
	out, _ := cmd.Output()
	v := cliVersionRe.FindString(string(out))
	if ctx.Err() == nil {
		cliVersions.Store(path, cliVersion{stamp: stamp, version: v})
	}
	return v
}
