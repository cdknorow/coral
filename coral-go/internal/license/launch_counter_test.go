package license

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// setLaunchCount forces the counter to a given launch number without having to
// call Increment that many times.
func setLaunchCount(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".launch-count"), []byte(strconv.Itoa(n)), 0644); err != nil {
		t.Fatalf("writing launch count: %v", err)
	}
}

// A brand-new user must never be asked to support Coral before Coral has done
// anything for them. This is the defect the task exists to fix: the old rule
// was count%3 == 1, which is true on launch 1.
func TestNoSupporterReminderBeforeCoralHasDeliveredAnything(t *testing.T) {
	dir := t.TempDir()
	lc := NewLaunchCounter(dir)

	if got := lc.Increment(); got != 1 {
		t.Fatalf("expected launch 1, got %d", got)
	}
	lc.RecordValueAnchor(false) // nothing has happened yet
	if lc.IsNagLaunch() {
		t.Fatal("a first-ever launch must not show the supporter reminder")
	}

	// And it stays quiet no matter how long they keep opening the app without
	// getting a result.
	for launch := 2; launch <= 20; launch++ {
		lc.Increment()
		lc.RecordValueAnchor(false)
		if lc.IsNagLaunch() {
			t.Fatalf("launch %d showed the supporter reminder with no value delivered", launch)
		}
	}
}

func TestSupporterReminderFollowsTheCadenceOnceValueIsDelivered(t *testing.T) {
	dir := t.TempDir()
	lc := NewLaunchCounter(dir)

	// Launches 1 and 2: nothing delivered, silent.
	lc.Increment()
	lc.RecordValueAnchor(false)
	lc.Increment()
	lc.RecordValueAnchor(false)
	if lc.IsNagLaunch() {
		t.Fatal("reminder fired before any value was delivered")
	}

	// Launch 3: the user has now launched their first agent. The launch that
	// anchors the cadence must itself stay quiet.
	lc.Increment()
	lc.RecordValueAnchor(true)
	if lc.IsNagLaunch() {
		t.Fatal("the anchoring launch must not itself show the reminder")
	}

	want := map[int]bool{4: false, 5: false, 6: true, 7: false, 8: false, 9: true, 10: false, 12: true}
	for launch := 4; launch <= 12; launch++ {
		lc.Increment()
		lc.RecordValueAnchor(true) // idempotent; the anchor stays at 3
		expected, checked := want[launch]
		if !checked {
			continue
		}
		if got := lc.IsNagLaunch(); got != expected {
			t.Errorf("launch %d: reminder shown = %v, want %v", launch, got, expected)
		}
	}
}

func TestValueAnchorIsRecordedOnceAndOnlyAfterValue(t *testing.T) {
	dir := t.TempDir()
	lc := NewLaunchCounter(dir)
	anchorFile := filepath.Join(dir, ".value-anchor")

	setLaunchCount(t, dir, 5)
	lc.RecordValueAnchor(false)
	if _, err := os.Stat(anchorFile); !os.IsNotExist(err) {
		t.Fatal("an anchor was written before any value was delivered")
	}

	lc.RecordValueAnchor(true)
	first, err := os.ReadFile(anchorFile)
	if err != nil {
		t.Fatalf("expected an anchor after value was delivered: %v", err)
	}
	if string(first) != "5" {
		t.Fatalf("anchor = %q, want the current launch 5", first)
	}

	// Later launches must not move the anchor, or the reminder would never
	// come due.
	setLaunchCount(t, dir, 40)
	lc.RecordValueAnchor(true)
	again, _ := os.ReadFile(anchorFile)
	if string(again) != "5" {
		t.Fatalf("anchor moved to %q; it must be recorded once", again)
	}
}

// IsNagLaunch is consulted more than once per launch (startup logging and the
// page handler), so it must not change anything.
func TestIsNagLaunchHasNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	lc := NewLaunchCounter(dir)
	setLaunchCount(t, dir, 6)
	lc.RecordValueAnchor(true) // anchor at 6

	setLaunchCount(t, dir, 9) // three launches later: due
	first := lc.IsNagLaunch()
	for i := 0; i < 5; i++ {
		if lc.IsNagLaunch() != first {
			t.Fatal("IsNagLaunch changed its answer when called repeatedly")
		}
	}
	if !first {
		t.Fatal("expected the reminder to be due 3 launches after the anchor")
	}
	if got := readCount(filepath.Join(dir, ".launch-count")); got != 9 {
		t.Fatalf("IsNagLaunch modified the launch count: %d", got)
	}
}

// A missing or corrupt state file must fail quiet, not fail loud at the user.
func TestUnreadableStateNeverProducesAReminder(t *testing.T) {
	dir := t.TempDir()
	lc := NewLaunchCounter(dir)

	if lc.IsNagLaunch() {
		t.Error("no state at all must not produce a reminder")
	}
	os.WriteFile(filepath.Join(dir, ".value-anchor"), []byte("not a number"), 0644)
	if lc.IsNagLaunch() {
		t.Error("a corrupt anchor must not produce a reminder")
	}
}
