package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// The "gemini" agent type is Antigravity compatibility: it runs agy (GetCLIInfo says so: its binary and installer are agy's), with agy's flags (--add-dir, --conversation, --mode,
// --dangerously-skip-permissions) and agy's model list (agy models). The real Gemini CLI takes other flags (--include-directories, --resume, --approval-mode, --yolo) and has no
// models command, so a cli_path_gemini that names it would start a program with arguments it does not understand (#5901). It is refused with a setup error instead.

// ErrRealGeminiCLI is why a configured agy or gemini path is refused when it is the Gemini CLI.
var ErrRealGeminiCLI = errors.New("this is the Gemini CLI, but Coral's gemini type is Antigravity compatibility and runs agy, with agy's flags and model list: install agy (Antigravity) and point this setting at it, or clear it")

// cliHelp is the start of what a program prints for --help; a variable so a test does not run a program. It runs with a minimal environment (the search path and the home folder, what a
// CLI needs to start, not Coral's own settings or secrets) and keeps only the first 4 KiB of what is printed, whatever the program writes.
var cliHelp = func(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := &headWriter{max: 4096}
	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.Env = helpEnv()
	cmd.Stdout, cmd.Stderr = out, out
	_ = cmd.Run() // a help that fails or times out is read as far as it got
	return out.buf.String()
}

// helpEnv is the environment a --help probe gets: PATH and the home folder, and what Windows needs to find its own programs.
func helpEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "LOCALAPPDATA"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// headWriter keeps the first max bytes written and discards the rest, always reporting the whole write, so the program is never blocked by a full buffer.
type headWriter struct {
	buf bytes.Buffer
	max int
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

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
