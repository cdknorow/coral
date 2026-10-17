//go:build windows

package agentprio

import "golang.org/x/sys/windows"

// lower puts pid in the BELOW_NORMAL priority class, unless it already runs
// at BELOW_NORMAL or IDLE (which would be raising it).
func lower(pid int) error {
	_, err := lowerChanged(pid)
	return err
}

// lowerChanged is lower, reporting whether the class was changed.
func lowerChanged(pid int) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	if cur, err := windows.GetPriorityClass(h); err == nil && alreadyLow(cur) {
		return false, nil
	}
	if err := windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS); err != nil {
		return false, err
	}
	return true, nil
}

// alreadyLow reports whether a priority class is BELOW_NORMAL or lower.
func alreadyLow(class uint32) bool {
	return class == windows.BELOW_NORMAL_PRIORITY_CLASS || class == windows.IDLE_PRIORITY_CLASS
}

// lowerGroup: Windows has no process groups; the step's own process is set.
func lowerGroup(pgid int) error { return lower(pgid) }

// descendants: on Windows only the process itself.
func descendants(pid int) []proc { return []proc{{pid: pid}} }

// descendantsOf: each process itself.
func descendantsOf(pids ...int) []proc {
	out := make([]proc, 0, len(pids))
	for _, pid := range pids {
		out = append(out, proc{pid: pid})
	}
	return out
}

// parentOf: not used on Windows (descendants never reports a parent).
func parentOf(pid int) (int, bool) { return 0, false }
