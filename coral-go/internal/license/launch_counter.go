package license

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// nagInterval is how many launches pass between supporter reminders, counted
// from the launch at which Coral first delivered a result.
const nagInterval = 3

// LaunchCounter tracks how many times Coral has been launched and the launch
// at which Coral first did something useful for the user. The supporter
// reminder is scheduled from the second of those, never the first: the ask
// follows a result, it does not precede one.
type LaunchCounter struct {
	path       string
	anchorPath string
}

func NewLaunchCounter(coralDir string) *LaunchCounter {
	return &LaunchCounter{
		path:       filepath.Join(coralDir, ".launch-count"),
		anchorPath: filepath.Join(coralDir, ".value-anchor"),
	}
}

// Increment bumps the counter and returns the new value.
func (lc *LaunchCounter) Increment() int {
	count := lc.read() + 1
	os.WriteFile(lc.path, []byte(strconv.Itoa(count)), 0644)
	return count
}

// RecordValueAnchor notes the current launch as the point at which Coral first
// delivered a result. Call it once per launch with whether any value milestone
// exists; it is idempotent and only the first qualifying launch is recorded.
//
// Anchoring here rather than nagging here is deliberate. The launch on which a
// user finally gets a result is the worst possible moment to interrupt with a
// pricing screen, so that launch starts the clock instead of triggering it.
func (lc *LaunchCounter) RecordValueAnchor(valueDelivered bool) {
	if !valueDelivered || lc.readAnchor() != 0 {
		return
	}
	count := lc.read()
	if count < 1 {
		count = 1
	}
	os.WriteFile(lc.anchorPath, []byte(strconv.Itoa(count)), 0644)
}

// IsNagLaunch reports whether the supporter reminder should be shown on this
// launch. It is a pure read with no side effects, so it is safe to call more
// than once per launch.
//
// It returns false until Coral has delivered a result — a brand-new user never
// sees it, however many times they open the app. After that it shows every
// nagInterval launches, and never on the anchoring launch itself.
func (lc *LaunchCounter) IsNagLaunch() bool {
	anchor := lc.readAnchor()
	if anchor == 0 {
		return false
	}
	since := lc.read() - anchor
	return since > 0 && since%nagInterval == 0
}

func (lc *LaunchCounter) read() int       { return readCount(lc.path) }
func (lc *LaunchCounter) readAnchor() int { return readCount(lc.anchorPath) }

func readCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}
