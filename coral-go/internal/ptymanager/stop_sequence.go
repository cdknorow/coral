package ptymanager

import "time"

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
// would be typed into that program's input. It returns when done closes or limit passes.
func politeStop(write func([]byte) error, done <-chan struct{}, hasChildren func() (bool, error), t stopTimings) {
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
			if busy, err := hasChildren(); err == nil && !busy {
				_ = write([]byte("exit\r"))
				return
			}
		}
	}
}
