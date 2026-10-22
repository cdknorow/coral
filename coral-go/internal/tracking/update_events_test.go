package tracking

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// #5952: update telemetry. Two events say what Coral can see (the notice was shown, the release page was opened), and upgrade says what the install was on before. Release numbers only,
// closed values, once per install, release and place, nothing with analytics off.

func TestVersionsAreReleaseNumbersAndNothingElse(t *testing.T) {
	for _, ok := range []string{"1.4.153", "0.9.0", "12.345.6789", "1.4.153-beta", "1.4.153-rc.1", "1.4.153-forDropbox"} {
		assert.True(t, ValidVersionProp(ok), ok)
	}
	for _, bad := range []string{"", "1.4", "v1.4.153", "1.4.153+abc", "0.0.0-dev+abcdef1.dirty", "1.4.153 ", " 1.4.153", "1.4.153\n", "1.4.x", "1.4.153-", "1.4.153-a b", "1.4.153-é", "1.4.153/..", "1.4.153;rm", "12345.1.1",
		strings.Repeat("1", 40), "1.4.153-" + strings.Repeat("a", 17), "/home/someone", "https://example.test/1.4.153"} {
		assert.False(t, ValidVersionProp(bad), "%q", bad)
	}
	typed, ok := sanitizeProps(EventUpdateNoticeShown, map[string]string{"to_version": "1.4.153", "source": "tray", "channel": "agentic", "extra": "x", "path": "/home/x"})
	require.True(t, ok)
	assert.Equal(t, map[string]any{"to_version": "1.4.153", "source": "tray", "channel": "agentic"}, typed, "unknown keys are dropped")
	typed, _ = sanitizeProps(EventUpdateOfferOpened, map[string]string{"to_version": "1.4.153+x", "source": "email", "channel": "beta", "method": "in_app"})
	assert.Empty(t, typed, "a version of another shape, a source, channel and method outside their sets are never sent")
	typed, _ = sanitizeProps(EventUpgrade, map[string]string{"from_version": "1.4.100", "to_version": "1.4.153"})
	assert.Equal(t, map[string]any{"from_version": "1.4.100"}, typed)
	assert.Equal(t, 4, SchemaVersion, "the update events are schema version 4")
}

func TestUpgradeCarriesTheVersionThatLastRanHere(t *testing.T) {
	rec, dir := newTestTracking(t)
	config.Version = "1.4.153"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".install_version"), []byte("1.4.100"), 0o600))
	TrackInstallAsync()
	waitForAsync()
	require.Equal(t, 1, rec.count(EventUpgrade), rec.names())
	for _, e := range rec.all() {
		if e.Event == EventUpgrade {
			assert.Equal(t, "1.4.100", e.Properties["from_version"])
			assert.Equal(t, "1.4.153", e.Properties["version"])
			assert.EqualValues(t, 4, e.Properties["schema_version"])
		}
	}
}

func TestUpgradeFromAVersionThatIsNotAReleaseNumberCarriesNoFromVersion(t *testing.T) {
	rec, dir := newTestTracking(t)
	config.Version = "1.4.153"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".install_version"), []byte("0.0.0-dev+abc123"), 0o600))
	TrackInstallAsync()
	waitForAsync()
	require.Equal(t, 1, rec.count(EventUpgrade))
	for _, e := range rec.all() {
		if e.Event == EventUpgrade {
			assert.NotContains(t, e.Properties, "from_version")
		}
	}
}

func TestTheUpdateNoticeIsSentOncePerReleaseAndPlace(t *testing.T) {
	rec, dir := newTestTracking(t)
	TrackUpdateNotice("1.4.154", UpdateSourceDashboard, UpdateChannelApproved)
	TrackUpdateNotice("1.4.154", UpdateSourceDashboard, UpdateChannelApproved) // the same release, the same place: once
	waitForAsync()
	assert.Equal(t, 1, rec.count(EventUpdateNoticeShown))
	TrackUpdateNotice("1.4.154", UpdateSourceTray, UpdateChannelApproved) // another place
	TrackUpdateNotice("1.4.155", UpdateSourceDashboard, UpdateChannelAgentic)
	waitForAsync()
	assert.Equal(t, 3, rec.count(EventUpdateNoticeShown))
	for _, e := range rec.all() { // they arrive in no fixed order: found by what they carry
		if e.Event != EventUpdateNoticeShown {
			continue
		}
		assert.NotContains(t, e.Properties, "method")
		switch e.Properties["to_version"] {
		case "1.4.155":
			assert.Equal(t, "agentic", e.Properties["channel"])
			assert.Equal(t, "dashboard", e.Properties["source"])
		case "1.4.154":
			assert.Equal(t, "approved", e.Properties["channel"])
		default:
			t.Fatalf("an unexpected version: %v", e.Properties)
		}
	}

	// a restart does not send it again: the state is on disk
	coralDir = dir
	TrackUpdateNotice("1.4.154", UpdateSourceDashboard, UpdateChannelApproved)
	waitForAsync()
	assert.Equal(t, 3, rec.count(EventUpdateNoticeShown))
}

func TestTheUpdateOfferCarriesTheDownloadLinkMethodAndIsOncePerReleaseAndPlace(t *testing.T) {
	rec, _ := newTestTracking(t)
	TrackUpdateOffer("1.4.154", UpdateSourceTray, UpdateChannelApproved)
	TrackUpdateOffer("1.4.154", UpdateSourceTray, UpdateChannelApproved)
	TrackUpdateNotice("1.4.154", UpdateSourceTray, UpdateChannelApproved) // the notice and the offer are two events, each once
	waitForAsync()
	assert.Equal(t, 1, rec.count(EventUpdateOfferOpened))
	assert.Equal(t, 1, rec.count(EventUpdateNoticeShown))
	for _, e := range rec.all() {
		if e.Event == EventUpdateOfferOpened {
			assert.Equal(t, "download_link", e.Properties["method"])
			assert.Equal(t, "tray", e.Properties["source"])
		}
	}
}

func TestInvalidUpdateEventsSendNothing(t *testing.T) {
	rec, dir := newTestTracking(t)
	TrackUpdateNotice("1.4.154+dev", UpdateSourceDashboard, UpdateChannelApproved)
	TrackUpdateNotice("", UpdateSourceDashboard, "")
	TrackUpdateNotice("1.4.154", "email", UpdateChannelApproved)
	TrackUpdateOffer("v1.4.154", UpdateSourceTray, "x")
	waitForAsync()
	assert.Zero(t, len(rec.all()))
	_, err := os.Stat(filepath.Join(dir, milestonesFileName))
	assert.True(t, os.IsNotExist(err), "and nothing is remembered")
	// an unknown channel is left out, the event is still sent
	TrackUpdateNotice("1.4.154", UpdateSourceTray, "nightly")
	waitForAsync()
	require.Equal(t, 1, rec.count(EventUpdateNoticeShown))
	assert.NotContains(t, rec.all()[0].Properties, "channel")
}

// With analytics off nothing is sent and no state is written: the notice is not consumed, so it is sent when analytics is turned on.
func TestUpdateEventsSendNothingAndRememberNothingWithAnalyticsOff(t *testing.T) {
	rec, dir := newTestTracking(t)
	SetTelemetryEnabled(false)
	t.Cleanup(func() { SetTelemetryEnabled(true) })
	TrackUpdateNotice("1.4.154", UpdateSourceDashboard, UpdateChannelApproved)
	TrackUpdateOffer("1.4.154", UpdateSourceDashboard, UpdateChannelApproved)
	TrackBrowserEvent(EventUpdateNoticeShown, map[string]string{"to_version": "1.4.154"})
	waitForAsync()
	assert.Zero(t, len(rec.all()))
	_, err := os.Stat(filepath.Join(dir, milestonesFileName))
	assert.True(t, os.IsNotExist(err), "no state file")
	SetTelemetryEnabled(true)
	TrackUpdateNotice("1.4.154", UpdateSourceDashboard, UpdateChannelApproved)
	waitForAsync()
	assert.Equal(t, 1, rec.count(EventUpdateNoticeShown), "not consumed while it was off")
}

// A page can report only a version and a channel: the source is the dashboard whatever it says, and it cannot name the method or anything else.
func TestTheDashboardReportsOnlyAVersionAndAChannel(t *testing.T) {
	rec, _ := newTestTracking(t)
	assert.True(t, BrowserEvents[EventUpdateNoticeShown] && BrowserEvents[EventUpdateOfferOpened])
	TrackBrowserEvent(EventUpdateNoticeShown, map[string]string{"to_version": "1.4.154", "channel": "agentic", "source": "tray", "method": "in_app", "x": "y"})
	TrackBrowserEvent(EventUpdateOfferOpened, map[string]string{"to_version": "1.4.154", "channel": "approved", "source": "tray", "method": "in_app"})
	TrackBrowserEvent(EventUpdateOfferOpened, map[string]string{"to_version": "not a version"})
	TrackBrowserEvent(EventUpdateNoticeShown, map[string]string{})
	waitForAsync()
	require.Equal(t, 2, len(rec.all()), rec.names())
	for _, e := range rec.all() {
		assert.Equal(t, "dashboard", e.Properties["source"], "the page cannot claim to be the tray")
		assert.Equal(t, "1.4.154", e.Properties["to_version"])
		if e.Event == EventUpdateOfferOpened {
			assert.Equal(t, "download_link", e.Properties["method"])
		} else {
			assert.NotContains(t, e.Properties, "method")
		}
	}
}
