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

// cliVersions caches versions by binary path, modification time and size, so an
// update of the CLI is noticed and an unchanged one is asked only once.
var cliVersions sync.Map

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
	key := fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
	if v, ok := cliVersions.Load(key); ok {
		return v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output()
	v := cliVersionRe.FindString(string(out))
	if ctx.Err() == nil {
		cliVersions.Store(key, v)
	}
	return v
}
