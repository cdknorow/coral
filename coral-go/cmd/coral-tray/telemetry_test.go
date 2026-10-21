package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/tracking"
)

// #5184: the tray's own events (install, app_opened, returned_24h, active week) went out whatever the saved Usage analytics setting said, because the tray
// process never reads it. The server child reports the setting it applied, and the tray sends only after hearing "on".
func TestTrayTelemetrySendsOnlyWhenTheChildSaysOn(t *testing.T) {
	t.Cleanup(func() { tracking.SetTelemetryEnabled(true) })
	for _, tc := range []struct {
		name string
		says []bool
		want int32
	}{
		{"off", []bool{false}, 0},
		{"on", []bool{true}, 1},
		{"off then off", []bool{false, false}, 0},
		{"on twice (a restarted child): one open", []bool{true, true}, 1},
		{"off, then on after a restart", []bool{false, true}, 1},
		{"on, then off after a restart: the first already went", []bool{true, false}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracking.SetTelemetryEnabled(false)
			var sent atomic.Int32
			f := trayTelemetry(func() { sent.Add(1) })
			for i, on := range tc.says {
				f(on)
				if i == 0 && !on {
					assert.False(t, tracking.TelemetryOn(), "an off keeps the tray's tracking off")
				}
			}
			assert.Equal(t, tc.want, sent.Load())
		})
	}
	// A child that says nothing (an old one, a crash before it was ready): nothing is sent, tracking stays off.
	tracking.SetTelemetryEnabled(false)
	assert.False(t, tracking.TelemetryOn())
}

// The supervisor hands on what the child says, once per start and before ready, and nothing when the child says nothing.
func TestSupervisorReportsTheChildsTelemetryLine(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want []bool
	}{
		{"telemetry-on", []bool{true}},
		{"telemetry-off", []bool{false}},
		{"serve", nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			s, _, _, _ := newTestSupervisor(t, tc.mode)
			var mu sync.Mutex
			var got []bool
			s.onTelemetry = func(on bool) { mu.Lock(); got = append(got, on); mu.Unlock() }
			ready := startAndRun(t, s)
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				t.Fatal("the child never said ready")
			}
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want, got)
		})
	}
}

// The whole path in one process: a tray that is told "off" posts nothing to the analytics endpoint, one told "on" posts its install events. The endpoint is a
// local capture server (a dev build's CORAL_TELEMETRY_TEST_SINK, so the real PostHog is never reached) and the data directory a temporary one.
// The helper process is this test binary with the sink in its environment from the start (the dev build reads it at init).
func TestHelperTrayTelemetry(t *testing.T) {
	mode := os.Getenv("TRAY_TELEMETRY_MODE")
	if mode == "" {
		t.Skip("only a helper process")
	}
	tracking.SetTelemetryEnabled(false) // as the tray's main does
	tracking.SetEntrypoint("coral-tray")
	tracking.SetCoralDir(os.Getenv("TRAY_TELEMETRY_DIR"))
	trayTelemetry(tracking.TrackInstallAsync)(mode == "on")
	time.Sleep(2 * time.Second) // events are sent from goroutines
	os.Exit(0)
}

func TestTrayWithTelemetryOffPostsNothing(t *testing.T) {
	if !tracking.SinkAvailable() {
		t.Skip("the capture seam is in dev builds only (-tags dev)")
	}
	run := func(mode string) int32 {
		var hits atomic.Int32
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			hits.Add(1)
			w.WriteHeader(200)
		}))
		defer sink.Close()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperTrayTelemetry$")
		cmd.Env = append(os.Environ(), "TRAY_TELEMETRY_MODE="+mode, "TRAY_TELEMETRY_DIR="+t.TempDir(), "CORAL_TELEMETRY_TEST_SINK="+sink.URL, "CORAL_TELEMETRY_DISABLED=")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return hits.Load()
	}
	assert.Zero(t, run("off"), "a tray told Usage analytics are off sends nothing")
	assert.Positive(t, run("on"), "and one told they are on sends its install events (the harness sees what a send looks like)")
}

// The tray's main starts its tracking off and hands the install call to the child's report; a direct TrackInstallAsync() call would bring the bug back.
func TestTrayMainDoesNotTrackOnItsOwn(t *testing.T) {
	b, err := os.ReadFile("main.go")
	require.NoError(t, err)
	src := string(b)
	assert.NotContains(t, src, "TrackInstallAsync()", "the tray sends its events only through trayTelemetry, after the child says on (#5184)")
	assert.Contains(t, src, "sup.onTelemetry = trayTelemetry(tracking.TrackInstallAsync)")
	off := strings.Index(src, "tracking.SetTelemetryEnabled(false)")
	start := strings.Index(src, "sup.start()")
	assert.True(t, off > 0 && start > off, "tracking is off before the server child starts")
}
