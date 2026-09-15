package background

import (
	"testing"
	"time"
)

// The poll cadence is a user setting on a hot path: too eager and a large
// repository spends its time scanning, too lax and the file list goes stale.
func TestNextWaitHonoursTheConfiguredInterval(t *testing.T) {
	tests := []struct {
		name        string
		configured  time.Duration
		wantWait    time.Duration
		wantPolling bool
	}{
		{"uses the configured interval", 5 * time.Minute, 5 * time.Minute, true},
		{"raises a too-eager interval to the floor", time.Second, MinGitPollInterval, true},
		{"exactly the floor is kept", MinGitPollInterval, MinGitPollInterval, true},
		{"zero pauses polling", 0, pausedRecheck, false},
		{"negative pauses polling", -time.Second, pausedRecheck, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &GitPoller{interval: time.Minute}
			p.SetIntervalFn(func() time.Duration { return tc.configured })

			wait, polling := p.nextWait()
			if wait != tc.wantWait {
				t.Errorf("wait = %v, want %v", wait, tc.wantWait)
			}
			if polling != tc.wantPolling {
				t.Errorf("polling = %v, want %v", polling, tc.wantPolling)
			}
		})
	}
}

// A paused poller must keep waking, or turning polling back on would need a
// restart to take effect.
func TestPausedPollerStillRechecksItsSetting(t *testing.T) {
	configured := time.Duration(0)
	p := &GitPoller{interval: time.Minute}
	p.SetIntervalFn(func() time.Duration { return configured })

	if wait, polling := p.nextWait(); polling || wait != pausedRecheck {
		t.Fatalf("paused poller: wait=%v polling=%v", wait, polling)
	}

	configured = 2 * time.Minute
	wait, polling := p.nextWait()
	if !polling || wait != 2*time.Minute {
		t.Fatalf("after re-enabling: wait=%v polling=%v, want 2m/true", wait, polling)
	}
}

// With no setting wired up at all, the built-in interval is used unchanged.
func TestNextWaitFallsBackToTheBuiltInInterval(t *testing.T) {
	p := &GitPoller{interval: 90 * time.Second}
	wait, polling := p.nextWait()
	if !polling || wait != 90*time.Second {
		t.Fatalf("wait=%v polling=%v, want 90s/true", wait, polling)
	}
}
