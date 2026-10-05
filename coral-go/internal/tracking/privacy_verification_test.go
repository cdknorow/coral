package tracking

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerification_TelemetryOptOut_InterceptAllEntryPoints independently verifies
// that opting out of telemetry completely prevents any network traffic to PostHog
// across all known tracking producers and entry points.
func TestVerification_TelemetryOptOut_InterceptAllEntryPoints(t *testing.T) {
	rec, _ := newTestTracking(t)

	// Phase 1: Baseline verification with telemetry enabled
	SetTelemetryEnabled(true)
	defer SetTelemetryEnabled(true)

	TrackEvent(EventSessionLaunched, nil)
	TrackOnce(EventFirstAgentLaunched, nil)
	TrackInstallAsync()
	waitForAsync()

	initialCount := len(rec.all())
	require.Greater(t, initialCount, 0, "expected events captured when telemetry is enabled")

	// Phase 2: Opt-out enforcement across all producers
	SetTelemetryEnabled(false)
	rec.mu.Lock()
	rec.events = nil
	rec.mu.Unlock()

	// 1. Install & Heartbeat
	TrackInstallAsync()

	// 2. Named events (supporter click, session launched, etc.)
	TrackEvent(EventSupporterCheckoutClicked, map[string]string{"surface": "modal"})
	TrackEvent(EventSessionLaunched, nil)
	TrackEvent(EventTeamLaunched, map[string]string{"agent_count": "3"})

	// 3. One-time milestone events
	TrackOnce(EventFirstTaskCompleted, nil)
	TrackOnce(EventFirstTeamLaunched, nil)

	// 4. Synchronous event capture
	trackEventSync("direct_sync_test", map[string]string{"k": "v"})

	waitForAsync()

	capturedAfterOptOut := rec.all()
	assert.Empty(t, capturedAfterOptOut, "expected ZERO events captured when telemetry is disabled")

	// Phase 3: Dynamic re-enablement verification
	SetTelemetryEnabled(true)
	TrackEvent(EventSessionLaunched, nil)
	waitForAsync()

	eventsAfterReenable := rec.all()
	require.Len(t, eventsAfterReenable, 1, "expected 1 event after re-enabling telemetry (nothing queued while opted out is replayed)")
	assert.Equal(t, EventSessionLaunched, eventsAfterReenable[0].Event)
}

// TestVerification_TelemetryFailClosed confirms that when telemetry is disabled,
// internal state updates never leak network requests even under edge conditions.
func TestVerification_TelemetryFailClosed(t *testing.T) {
	rec, _ := newTestTracking(t)
	SetTelemetryEnabled(false)
	defer SetTelemetryEnabled(true)

	// Verify telemetryEnabled() helper
	assert.False(t, telemetryEnabled(), "telemetryEnabled should return false when disabled")

	// Try firing return visit sync
	trackReturnVisitSync(telemetryGen.Load())
	waitForAsync()

	assert.Empty(t, rec.all(), "return visit sync should not send network requests when disabled")
}
