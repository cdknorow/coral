package ptymanager

import (
	"strings"
	"time"

	"github.com/subgentic/coral/internal/terminalinput"
)

// stopTimings are the pauses of a polite stop where Terminate types into the console (Windows). A raw-mode CLI takes the
// first Ctrl-C as "clear the input / press again to exit" (Claude Code, Codex), so one Ctrl-C left the stop to wait out the
// whole grace and then kill it: the agent never got to shut down.
type stopTimings struct {
	secondAfter time.Duration // the second Ctrl-C, if the process is still there
	poll        time.Duration // how often a shell with nothing left to run is looked for
	limit       time.Duration // when the polite stop gives up and leaves the rest to the force kill
}

var defaultStopTimings = stopTimings{secondAfter: 600 * time.Millisecond, poll: 150 * time.Millisecond, limit: 4 * time.Second}

// politeStop is what follows a first Ctrl-C: a second one after secondAfter if the process is still there, and then,
// once the process the terminal started has no program left running under it (the agent has exited and its shell is back
// at the prompt), "exit" and Enter to end the shell. "exit" is never typed while a program is still running, since it
// would be typed into that program's input, and never when the process the terminal started is not a known shell
// (rootIsShell): an agent started directly as the root has no child of its own, and "exit" would be a message to it.
// An unknown root (rootIsShell false) is not a shell. It returns when done closes or limit passes.
func politeStop(write func([]byte) error, done <-chan struct{}, hasChildren func() (bool, error), rootIsShell func() bool, t stopTimings) {
	deadline := time.NewTimer(t.limit)
	defer deadline.Stop()
	second := time.NewTimer(t.secondAfter)
	defer second.Stop()
	select {
	case <-done:
		return
	case <-deadline.C:
		return
	case <-second.C:
	}
	if write([]byte{0x03}) != nil {
		return
	}
	tick := time.NewTicker(t.poll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline.C:
			return
		case <-tick.C:
			if busy, err := hasChildren(); err == nil && !busy && rootIsShell() {
				_ = write([]byte("exit\r"))
				return
			}
		}
	}
}

// noteStopWrite is write, with each write recorded in the input debug log as Coral's own control input (terminalinput.Internal) for
// the session name: the second Ctrl-C and the "exit" line of a polite stop (#5706; the first Ctrl-C is recorded by session.killWithin).
// The record is the log's non-blocking enqueue (a full queue drops the line), so a stuck log writer cannot delay the stop.
func noteStopWrite(name string, write func([]byte) error) func([]byte) error {
	return func(b []byte) error {
		terminalinput.Record(terminalinput.Internal, name, terminalinput.Raw, string(b))
		return write(b)
	}
}

// stopNamer is implemented by a terminal process whose stop types into its console (Windows): the session tells it its name before
// Terminate, so the stop's own writes are recorded under it.
type stopNamer interface{ setStopName(name string) }

// isShellImage says whether an executable's path or name is one of the Windows shells a terminal is started with: powershell.exe,
// pwsh.exe or cmd.exe, whatever the folder and the case. Anything else, an empty name included, is not a shell.
func isShellImage(image string) bool {
	name := strings.ToLower(image)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "powershell.exe", "pwsh.exe", "cmd.exe":
		return true
	}
	return false
}
