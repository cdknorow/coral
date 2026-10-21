package tmux

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/subgentic/coral/internal/logdedupe"
)

// run executes a tmux command. If the args target a session (-t flag),
// it routes to the correct socket using the session cache.
// runObserver, when set (tests only), sees every tmux command run and listPanesOnSocket issue. It is
// read without a lock: a test sets it before starting anything that runs tmux and clears it after.
var runObserver func(args []string)

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	if runObserver != nil {
		runObserver(args)
	}
	if len(args) > 0 && paneChanging[args[0]] {
		// After it has run, so a list taken while it ran isn't kept either (the generation).
		defer c.forgetPaneStates()
	}
	// Check if this command targets a session — route via cache
	if c.FallbackToDefault && c.SocketPath != "" {
		for i, arg := range args {
			if arg == "-t" && i+1 < len(args) {
				target := args[i+1]
				session := sessionFromTarget(target)
				c.socketsMu.RLock()
				socket, ok := c.sessionSockets[session]
				c.socketsMu.RUnlock()
				if ok && socket != c.SocketPath {
					return c.runOnSocket(ctx, socket, args...)
				}
				break
			}
		}
	}
	// Default: use primary socket, fallback on error
	out, err := c.runOnSocket(ctx, c.SocketPath, args...)
	if err != nil && c.FallbackToDefault && c.SocketPath != "" {
		if fallbackOut, fallbackErr := c.runOnSocket(ctx, "", args...); fallbackErr == nil {
			return fallbackOut, nil
		}
	}
	return out, err
}

// runOnSocket runs a tmux command on a specific socket. Empty socketPath uses the default.
func (c *Client) runOnSocket(ctx context.Context, socketPath string, args ...string) (string, error) {
	if len(args) > 0 {
		countExec(args[0])
	}
	if socketPath != "" {
		args = append(socketArgs(socketPath), args...)
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	out, err := cmd.Output()
	noteTmuxResult(err, bin, socketPath)
	if err != nil {
		logExecFailure(bin, socketPath, args, err)
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// execFailures coalesces repeated tmux failures (#4112): the same command failing the same way
// on the same pane (a send to a pane that is gone, again and again) logs once, then once every
// execFailureEvery with how many were held back.
var execFailures = logdedupe.New(execFailureEvery)

const execFailureEvery = 5 * time.Minute

func logExecFailure(bin, socketPath string, args []string, err error) {
	stderr := execStderr(err)
	sub, target := tmuxCommandName(args)
	ok, heldBack := execFailures.Allow(socketPath + "\x00" + sub + "\x00" + target + "\x00" + err.Error() + "\x00" + stderr)
	if !ok {
		return
	}
	more := ""
	if heldBack > 0 {
		more = fmt.Sprintf(" (and %d more like it in the last %s)", heldBack, execFailureEvery)
	}
	log.Printf("[tmux] exec failed: bin=%q socket=%q %s err=%v stderr=%q%s", bin, socketPath, describeTmuxCommand(args), err, stderr, more)
}

// tmuxCommandName is a tmux command's subcommand and -t target, past the socket arguments.
func tmuxCommandName(args []string) (sub, target string) {
	i := 0
	for i+1 < len(args) && (args[i] == "-S" || args[i] == "-f" || args[i] == "-L") {
		i += 2
	}
	if i >= len(args) {
		return "", ""
	}
	for j := i + 1; j+1 < len(args); j++ {
		if args[j] == "-t" {
			return args[i], args[j+1]
		}
	}
	return args[i], ""
}

// describeTmuxCommand names a tmux command for a failure log line without its values (#4112,
// Security's #4107 audit): the subcommand, its -t target, and how many other arguments it had
// and their bytes. The other values are typed keystrokes and text (send-keys -l), buffer
// contents and names, a session's launch command line and format strings: a failed send
// logged what was typed. The socket arguments are logged on their own.
func describeTmuxCommand(args []string) string {
	i := 0
	for i+1 < len(args) && (args[i] == "-S" || args[i] == "-f" || args[i] == "-L") {
		i += 2
	}
	if i >= len(args) {
		return "cmd=none"
	}
	target, n, size := "", 0, 0
	for j := i + 1; j < len(args); j++ {
		if args[j] == "-t" && j+1 < len(args) {
			target = args[j+1]
			j++
			continue
		}
		n++
		size += len(args[j])
	}
	return fmt.Sprintf("cmd=%s target=%q other_args=%d bytes=%d", args[i], target, n, size)
}

// execStderrMax caps how much of tmux's stderr a failure log line carries.
const execStderrMax = 300

// execStderr returns what tmux wrote to stderr before failing, as one capped line,
// so an exec failure explains itself ("server exited unexpectedly", "no server
// running", "duplicate session"). cmd.Output keeps it in the ExitError.
func execStderr(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ""
	}
	s := strings.Join(strings.Fields(string(ee.Stderr)), " ")
	// tmux repeats a key it doesn't know ("unknown key: <key>"): the key is what was typed.
	if strings.HasPrefix(s, "unknown key:") {
		s = "unknown key"
	}
	if len(s) > execStderrMax {
		cut := execStderrMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut-- // never split a character
		}
		s = s[:cut] + "…"
	}
	return s
}

// sessionFromTarget extracts the session name from a tmux target (e.g. "name.0" → "name").
// A leading "=" (exact match, as in has-session -t =name) is not part of the name.
func sessionFromTarget(target string) string {
	target = strings.TrimPrefix(target, "=")
	if i := strings.IndexAny(target, ".:"); i > 0 {
		return target[:i]
	}
	return target
}

// execCounts counts the tmux commands Coral ran, by command name (#4049): the busy-server
// profile reads them (ExecCounts, served on the loopback pprof port) for execs per minute.
var execCounts sync.Map // string -> *atomic.Int64

func countExec(name string) {
	addExecCount(name)
	if name == "capture-pane" {
		// Which code captured (#4077): capture-pane is the top tmux exec, from many callers.
		addExecCount("capture-pane<" + execCaller())
	}
}

func addExecCount(name string) {
	v, ok := execCounts.Load(name)
	if !ok {
		v, _ = execCounts.LoadOrStore(name, new(atomic.Int64))
	}
	v.(*atomic.Int64).Add(1)
}

// execCaller names the first function outside the tmux client and the thin backend wrappers
// that only forward a capture, e.g. "routes.(*SessionsHandler).TeamAvailability".
func execCaller() string {
	var pcs [24]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	for {
		f, more := frames.Next()
		fn := strings.TrimPrefix(f.Function, "github.com/subgentic/coral/internal/")
		if fn != "" && !strings.HasPrefix(fn, "tmux.") && !strings.HasPrefix(fn, "ptymanager.(*TmuxSessionTerminal).") &&
			!strings.HasPrefix(fn, "background.(*TmuxRuntime).") {
			return fn
		}
		if !more {
			return "?"
		}
	}
}

// ExecCounts is how many of each tmux command Coral has run since it started.
func ExecCounts() map[string]int64 {
	out := map[string]int64{}
	execCounts.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}
