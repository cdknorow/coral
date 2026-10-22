package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
)

// The "gemini" agent type is Antigravity compatibility: it runs agy (GetCLIInfo says so: its binary and installer are agy's), with agy's flags (--add-dir, --conversation, --mode,
// --dangerously-skip-permissions) and agy's model list (agy models). The real Gemini CLI takes other flags (--include-directories, --resume, --approval-mode, --yolo) and has no
// models command, so a cli_path_gemini that names it would start a program with arguments it does not understand (#5901). It is refused with a setup error instead.

// ErrRealGeminiCLI is why a configured agy or gemini path is refused when it is the Gemini CLI.
var ErrRealGeminiCLI = errors.New("this is the Gemini CLI, but Coral's gemini type is Antigravity compatibility and runs agy, with agy's flags and model list: install agy (Antigravity) and point this setting at it, or clear it")

// cliHelp is the start of what a program prints for --help; a variable so a test does not run a program.
var cliHelp = func(path string) string {
	return cliHelpWithTimeout(path, 5*time.Second)
}

// Content tests allow scheduling headroom without changing the setup deadline.
func cliHelpWithTimeout(path string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out cliHelpOutput
	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.Env = cliHelpEnv()
	cmd.Stdout, cmd.Stderr = &out, &out
	executil.NoCwdSearch(cmd) // Windows: a shim looks for its interpreter in the current folder first (#183)
	executil.HideWindow(cmd)  // Windows: no console window of its own when Coral has none (#211)
	_ = cmd.Run()             // a help that fails or times out is read as far as it got
	return out.String()
}

// Only the executable search path and home reach this setup probe. Go's os/exec
// adds SYSTEMROOT on Windows; credentials and runtime settings are not inherited.
func cliHelpEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

const cliHelpOutputLimit = 4096

// cliHelpOutput retains a fixed-size prefix while draining both output streams.
type cliHelpOutput struct {
	buf [cliHelpOutputLimit]byte
	n   int
}

func (b *cliHelpOutput) Write(p []byte) (int, error) {
	b.n += copy(b.buf[b.n:], p)
	return len(p), nil
}

func (b *cliHelpOutput) String() string { return string(b.buf[:b.n]) }

// isRealGeminiHelp tells the Gemini CLI's help ("Usage: gemini [options] [command]", "Gemini CLI - Defaults to interactive mode") from agy's ("Usage of agy:").
func isRealGeminiHelp(help string) bool {
	return strings.Contains(help, "Gemini CLI") && !strings.HasPrefix(strings.TrimSpace(help), "Usage of agy")
}

var geminiGuardCache sync.Map // path + size + mtime -> bool (is the real Gemini CLI)

// CheckNotRealGemini is nil unless agentType runs agy (agy, antigravity, gemini) and the program at the resolved path is the real Gemini CLI. The answer is kept per file (path, size and
// modification time), so a launch does not run the program every time; a program that cannot be asked is not refused (the launch is as it was).
func CheckNotRealGemini(agentType, resolved string) error {
	if agentType != at.Agy && agentType != at.Antigravity && agentType != at.Gemini {
		return nil
	}
	key := resolved
	if fi, err := os.Stat(resolved); err == nil {
		key = fmt.Sprintf("%s|%d|%d", resolved, fi.Size(), fi.ModTime().UnixNano())
	}
	if v, ok := geminiGuardCache.Load(key); ok {
		if v.(bool) {
			return ErrRealGeminiCLI
		}
		return nil
	}
	real := isRealGeminiHelp(cliHelp(resolved))
	geminiGuardCache.Store(key, real)
	if real {
		return ErrRealGeminiCLI
	}
	return nil
}
