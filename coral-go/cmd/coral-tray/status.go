package main

import (
	"sync"

	"fyne.io/systray"
	"github.com/gen2brain/beeep"
)

// trayStatus shows the supervised server's state in the tray (#4163): nothing extra while it
// runs; while it restarts after a crash, why; when it stopped or could not start, why, with
// Open Log and Retry. A state set before the menu exists is shown when it does.
type trayStatus struct {
	mu                        sync.Mutex
	state                     serverState
	reason                    string
	reasonItem, logItem, retr *systray.MenuItem
}

func (t *trayStatus) set(st serverState, reason string) {
	t.mu.Lock()
	changed := st != t.state || reason != t.reason
	t.state, t.reason = st, reason
	t.applyLocked()
	t.mu.Unlock()
	if changed && st == stateFailed {
		beeep.Notify("Coral", statusTitle("Coral's server is not running: ", reason), "")
	}
}

func (t *trayStatus) running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state == stateRunning
}

func (t *trayStatus) attach(reason, logItem, retry *systray.MenuItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reasonItem, t.logItem, t.retr = reason, logItem, retry
	t.applyLocked()
}

func (t *trayStatus) applyLocked() {
	if t.reasonItem == nil {
		return
	}
	show := func(item *systray.MenuItem, on bool) {
		if on {
			item.Show()
		} else {
			item.Hide()
		}
	}
	switch t.state {
	case stateRestarting:
		t.reasonItem.SetTitle(statusTitle("Restarting after a crash: ", t.reason))
		systray.SetTooltip("Coral — restarting")
		show(t.reasonItem, true)
		show(t.logItem, true)
		show(t.retr, false)
	case stateFailed, stateStopped:
		prefix := "Not running: "
		if t.state == stateStopped {
			prefix = "Stopped: "
		}
		t.reasonItem.SetTitle(statusTitle(prefix, t.reason))
		systray.SetTooltip("Coral — Error")
		show(t.reasonItem, true)
		show(t.logItem, true)
		show(t.retr, true)
	default:
		systray.SetTooltip("Coral Dashboard")
		show(t.reasonItem, false)
		show(t.logItem, false)
		show(t.retr, false)
	}
}
