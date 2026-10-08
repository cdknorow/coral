package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIVersionProbeBoundsExcessiveOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli")
	script := "#!/bin/sh\nprintf '%s' '" + strings.Repeat("x", 65536) + "'\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0700))
	version, probe := probeCLIVersion(context.Background(), path)
	assert.Equal(t, probeOK, probe)
	assert.Equal(t, strings.Repeat("x", maxCLIVersionBytes), version)
}

// fakeCLIs points the CLI/tmux seams at scripts in a temp dir, so no real
// executable, install location or user directory is ever consulted.
type fakeCLIs struct {
	dir     string
	present map[string]string // binary name -> script path
}

func useFakeCLIs(t *testing.T) *fakeCLIs {
	t.Helper()
	f := &fakeCLIs{dir: t.TempDir(), present: map[string]string{}}
	prevLook, prevCommon, prevTimeout, prevTmux := cliLookPath, cliCommonPath, cliProbeTimeout, tmuxProbe
	cliLookPath = func(name string) (string, error) {
		if p, ok := f.present[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	cliCommonPath = func(string) string { return "" }
	// Generous: a healthy fake CLI is a shell script, and on a loaded machine one that takes
	// over a few hundred milliseconds must not be reported as a timeout. The test that wants a
	// timeout shortens this itself.
	cliProbeTimeout = 5 * time.Second
	tmuxProbe = func() (string, bool) { return "", false }
	t.Cleanup(func() {
		cliLookPath, cliCommonPath, cliProbeTimeout, tmuxProbe = prevLook, prevCommon, prevTimeout, prevTmux
	})
	return f
}

func (f *fakeCLIs) install(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(f.dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	f.present[name] = path
}

func newSystemHandlerForTest(t *testing.T) *SystemHandler {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/sys.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return NewSystemHandler(db, &config.Config{LogDir: t.TempDir()})
}

func cliCheck(t *testing.T, h *SystemHandler, query string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/system/cli-check?"+query, nil)
	rec := httptest.NewRecorder()
	h.CLICheck(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func prereqEvents(sink *analyticsSink) []map[string]any {
	return sink.named(tracking.EventPrerequisiteCheck)
}

func TestCLICheckReportsAvailableMissingProbeFailedAndTimeoutSeparately(t *testing.T) {
	sink := captureAnalytics(t)
	cli := useFakeCLIs(t)
	h := newSystemHandlerForTest(t)

	// Available: found, version first line only.
	cli.install(t, "claude", `printf '2.1.0 (Claude Code)\nsecond line\n'`)
	r := cliCheck(t, h, "type=claude")
	assert.Equal(t, true, r["found"])
	assert.Equal(t, "available", r["status"])
	assert.Equal(t, "ok", r["probe"])
	assert.Equal(t, "2.1.0 (Claude Code)", r["version"])
	assert.NotEmpty(t, r["path"])

	// Found but the version probe fails: still found:true, reported separately.
	cli.install(t, "codex", `echo "boom: secret detail" >&2; exit 3`)
	r = cliCheck(t, h, "type=codex")
	assert.Equal(t, true, r["found"], "the found contract is preserved")
	assert.Equal(t, "probe_failed", r["status"])
	assert.Equal(t, "failed", r["probe"])
	assert.Equal(t, "", r["version"])
	assert.NotContains(t, r, "error", "no raw probe error is exposed")

	// Found but the version probe hangs: a bounded timeout, reported as such.
	cli.install(t, "codex", `sleep 5`)
	cliProbeTimeout = 300 * time.Millisecond
	start := time.Now()
	r = cliCheck(t, h, "type=codex&source=cli_recheck")
	assert.Less(t, time.Since(start), 3*time.Second, "the probe is bounded")
	assert.Equal(t, true, r["found"])
	assert.Equal(t, "timeout", r["status"])
	assert.Equal(t, "timeout", r["probe"])

	// Missing: not found anywhere, with the install hint but no installer claim.
	delete(cli.present, "claude")
	r = cliCheck(t, h, "type=claude")
	assert.Equal(t, false, r["found"])
	assert.Equal(t, "missing", r["status"])
	assert.NotContains(t, r, "probe")
	assert.NotEmpty(t, r["install_command"])

	// Analytics: one event per distinct result, fixed values only.
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) >= 4 }, 3*time.Second, 5*time.Millisecond)
	type key struct{ tool, status, source string }
	got := map[key]bool{}
	for _, e := range prereqEvents(sink) {
		got[key{e["tool"].(string), e["status"].(string), e["source"].(string)}] = true
	}
	assert.Equal(t, map[key]bool{
		{"claude", "available", "cli_check"}:   true,
		{"codex", "probe_failed", "cli_check"}: true,
		{"codex", "timeout", "cli_recheck"}:    true,
		{"claude", "missing", "cli_check"}:     true,
	}, got)
	raw := sink.allRaw()
	for _, leaked := range []string{cli.dir, "2.1.0", "secret detail", "Claude Code", "installer"} {
		assert.NotContains(t, raw, leaked)
	}
}

func TestPrerequisiteEventsAreDedupedPerRunButTransitionsAreReported(t *testing.T) {
	sink := captureAnalytics(t)
	cli := useFakeCLIs(t)
	h := newSystemHandlerForTest(t)

	for i := 0; i < 5; i++ { // polling
		cliCheck(t, h, "type=claude")
	}
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) >= 1 }, 3*time.Second, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	assert.Len(t, prereqEvents(sink), 1, "repeated identical checks send once per run")

	cli.install(t, "claude", `echo 1.0.0`) // the user installs it
	for i := 0; i < 3; i++ {
		cliCheck(t, h, "type=claude")
	}
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) >= 2 }, 3*time.Second, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	events := prereqEvents(sink)
	assert.Len(t, events, 2, "missing then available are both reported, once each")
	statuses := []any{events[0]["status"], events[1]["status"]}
	assert.ElementsMatch(t, []any{"missing", "available"}, statuses)
}

func TestPrerequisiteTrackingSkipsUntrackedChecksAndDoesNotConsumeWhileDisabled(t *testing.T) {
	sink := captureAnalytics(t)
	cli := useFakeCLIs(t)
	cli.install(t, "claude", `echo 1.0.0`)
	h := newSystemHandlerForTest(t)

	// A caller-supplied binary, an untracked agent type and a cancelled request
	// are not prerequisite observations.
	cliCheck(t, h, "binary="+cli.present["claude"])
	cliCheck(t, h, "type=gemini")
	cli.install(t, "gemini", `echo 1`)
	cliCheck(t, h, "type=gemini")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude", nil).WithContext(ctx)
	h.CLICheck(httptest.NewRecorder(), req)
	time.Sleep(80 * time.Millisecond)
	assert.Empty(t, prereqEvents(sink))

	// Opted out: nothing is sent and the observation is NOT remembered, so it
	// is still reported once telemetry is enabled again.
	tracking.SetTelemetryEnabled(false)
	cliCheck(t, h, "type=claude")
	time.Sleep(80 * time.Millisecond)
	assert.Empty(t, prereqEvents(sink))
	tracking.SetTelemetryEnabled(true)
	cliCheck(t, h, "type=claude")
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) == 1 }, 3*time.Second, 5*time.Millisecond)
}

func TestStatusReportsTmuxAvailabilityEffectiveBackendAndKeepsExistingFields(t *testing.T) {
	sink := captureAnalytics(t)
	useFakeCLIs(t)
	h := newSystemHandlerForTest(t)
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		// Re-check each time: this test changes what discovery finds.
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status?recheck=1", nil))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	s := get()
	for _, existing := range []string{"startup_complete", "version", "tier_name", "tmux_available", "tmux_path", "tmux_install_command"} {
		assert.Contains(t, s, existing, "existing field preserved")
	}
	assert.Equal(t, false, s["tmux_available"])
	assert.Equal(t, "missing", s["tmux_status"])
	assert.Equal(t, "unknown", s["effective_backend"])
	assert.Equal(t, false, s["tmux_required"])

	// tmux appears (the user installed it); frequent polling sends one event per result.
	tmuxProbe = func() (string, bool) { return "/usr/bin/tmux", true }
	for i := 0; i < 4; i++ {
		s = get()
	}
	assert.Equal(t, true, s["tmux_available"])
	assert.Equal(t, "available", s["tmux_status"])
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) >= 2 }, 3*time.Second, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	events := prereqEvents(sink)
	assert.Len(t, events, 2)
	for _, e := range events {
		assert.Equal(t, "tmux", e["tool"])
		assert.Equal(t, "system_status", e["source"])
	}
	assert.NotContains(t, sink.allRaw(), "/usr/bin/tmux")

	// The effective backend decides whether tmux is required; PTY is never blocked.
	h.SetTerminal(ptymanager.NewPTYSessionTerminal(ptymanager.NewPTYBackend()))
	s = get()
	assert.Equal(t, "pty", s["effective_backend"])
	assert.Equal(t, false, s["tmux_required"])
	h.SetTerminal(ptymanager.NewTmuxSessionTerminal(tmux.NewClient()))
	s = get()
	assert.Equal(t, "tmux", s["effective_backend"])
	assert.Equal(t, true, s["tmux_required"])
}

func TestPrerequisiteCheckEventIsTypedAndRejectsOtherValues(t *testing.T) {
	sink := captureAnalytics(t)
	for _, bad := range [][3]string{{"python", "available", "cli_check"}, {"tmux", "installer_failed", "cli_check"}, {"tmux", "missing", "ui"}} {
		tracking.TrackPrerequisite(bad[0], bad[1], bad[2])
	}
	time.Sleep(80 * time.Millisecond)
	assert.Empty(t, prereqEvents(sink))
	tracking.TrackPrerequisite("tmux", "missing", "cli_check")
	require.Eventually(t, func() bool { return len(prereqEvents(sink)) == 1 }, 3*time.Second, 5*time.Millisecond)
}

// Routine status polls must not repeat tmux discovery (which can start a login
// shell); an explicit re-check does, and the cache expires.
func TestStatusCachesTmuxDiscoveryAndRecheckBypassesIt(t *testing.T) {
	captureAnalytics(t)
	useFakeCLIs(t)
	h := newSystemHandlerForTest(t)
	calls := 0
	tmuxProbe = func() (string, bool) { calls++; return "", false }
	status := func(path string, hdr map[string]string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.Status(rec, req)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	for i := 0; i < 20; i++ { // dashboard polling
		status("/api/system/status", nil)
	}
	assert.Equal(t, 1, calls, "routine polls reuse the cached discovery")

	tmuxProbe = func() (string, bool) { calls++; return "/opt/tmux", true }
	assert.Equal(t, false, status("/api/system/status", nil)["tmux_available"], "still the cached answer")
	assert.Equal(t, true, status("/api/system/status?recheck=1", nil)["tmux_available"], "explicit re-check bypasses the cache")
	assert.Equal(t, 2, calls)
	// A cache:"no-store" fetch ("Check again") is also an explicit re-check.
	tmuxProbe = func() (string, bool) { calls++; return "", false }
	assert.Equal(t, false, status("/api/system/status", map[string]string{"Cache-Control": "no-cache"})["tmux_available"])
	assert.Equal(t, 3, calls)

	// The cache expires on its own.
	h.tmuxCacheTTL = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	status("/api/system/status", nil)
	assert.Equal(t, 4, calls)
}
