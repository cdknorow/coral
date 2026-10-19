//go:build !windows

package startup

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4937 part 2: end to end, a dev build pointed at a fake capture endpoint through the dev-only
// CORAL_TELEMETRY_TEST_SINK seam (#4935) sends install and app_opened with the safe standard properties
// and nothing that identifies anyone; with the opt-out it sends nothing. It never reaches the real
// PostHog (the sink is a local httptest server) or the real ~/.coral (an isolated HOME and data dir).

type capturedEvent struct {
	event, apiKey, distinctID string
	props                     map[string]any
}

func telemetryModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func TestTelemetrySinkE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a dev coral binary")
	}
	bin := filepath.Join(t.TempDir(), "coral")
	build := exec.Command("go", "build", "-tags", "dev", "-o", bin, "./cmd/coral")
	build.Dir = telemetryModuleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dev coral: %v\n%s", err, out)
	}

	// run starts the dev binary against a fresh fake sink and returns the events captured within the deadline.
	run := func(t *testing.T, extraEnv []string) []capturedEvent {
		var mu sync.Mutex
		var events []capturedEvent
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var p struct {
				APIKey     string         `json:"api_key"`
				Event      string         `json:"event"`
				DistinctID string         `json:"distinct_id"`
				Properties map[string]any `json:"properties"`
			}
			if json.Unmarshal(body, &p) == nil && p.Event != "" {
				mu.Lock()
				events = append(events, capturedEvent{p.Event, p.APIKey, p.DistinctID, p.Properties})
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		iso := t.TempDir()
		// The dev binary probes `go`, which writes a read-only module cache under this HOME; make the tree
		// writable before t.TempDir removes it, so the test leaves nothing behind. Runs before the removal (LIFO).
		t.Cleanup(func() {
			_ = filepath.WalkDir(iso, func(p string, _ os.DirEntry, err error) error {
				if err == nil {
					_ = os.Chmod(p, 0o755)
				}
				return nil
			})
		})
		home := filepath.Join(iso, "home")
		data := filepath.Join(iso, "data")
		stubs := filepath.Join(iso, "stub-bin")
		for _, d := range []string{home, data, stubs} {
			require.NoError(t, os.MkdirAll(d, 0o700))
		}
		for _, b := range []string{"claude", "codex", "pi", "agy", "antigravity", "gemini", "tmux"} {
			require.NoError(t, os.WriteFile(filepath.Join(stubs, b), []byte("#!/bin/sh\necho 1.0.0\n"), 0o700))
		}
		port := freePort(t)
		cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-browser", "--cloud-url", "off")
		cmd.Env = append([]string{
			"HOME=" + home, "USERPROFILE=" + home,
			"TMPDIR=" + iso,
			"CORAL_DATA_DIR=" + data,
			"CORAL_NO_DEMO_SEED=1", "CORAL_NO_WELCOME=1",
			"CORAL_CLOUD_URL=",
			"CORAL_TELEMETRY_TEST_SINK=" + srv.URL + "/capture/",
			"PATH=" + stubs + ":/usr/bin:/bin:/usr/sbin:/sbin",
		}, extraEnv...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		require.NoError(t, cmd.Start())
		defer func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
		}()

		deadline := time.Now().Add(30 * time.Second)
		base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
		for time.Now().Before(deadline) { // the server is up
			if resp, err := http.Get(base); err == nil {
				resp.Body.Close()
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		has := func() bool {
			mu.Lock()
			defer mu.Unlock()
			var i, a bool
			for _, e := range events {
				if e.event == "install" {
					i = true
				}
				if e.event == "app_opened" {
					a = true
				}
			}
			return i && a
		}
		for time.Now().Before(deadline) && !has() {
			time.Sleep(200 * time.Millisecond)
		}
		// With the opt-out, nothing should ever arrive: wait a fixed moment so a stray send would be caught.
		if len(extraEnv) > 0 {
			time.Sleep(4 * time.Second)
		}
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedEvent, len(events))
		copy(out, events)
		return out
	}

	// Case A: a plain dev build sends install and app_opened with the safe standard properties, no 'internal'.
	got := run(t, nil)
	names := map[string]bool{}
	for _, e := range got {
		names[e.event] = true
	}
	require.True(t, names["install"], "install was sent (got %v)", names)
	require.True(t, names["app_opened"], "app_opened was sent (got %v)", names)
	for _, e := range got {
		assert.Equal(t, "dev-test-sink", e.apiKey, "%s carries the dev test key, so it is the keyed path", e.event)
		assert.NotEmpty(t, e.distinctID, "%s has a distinct_id", e.event)
		for _, k := range []string{"version", "edition", "os", "arch", "platform", "schema_version", "run_id", "entrypoint"} {
			assert.Contains(t, e.props, k, "%s carries the standard property %s", e.event, k)
		}
		assert.NotContains(t, e.props, "internal", "%s has no 'internal' property (there is no internal concept in the repo)", e.event)
	}

	// Case B: with the opt-out, the same dev build sends nothing.
	optOut := run(t, []string{"CORAL_TELEMETRY_DISABLED=1"})
	var optNames []string
	for _, e := range optOut {
		optNames = append(optNames, e.event)
	}
	assert.Empty(t, optOut, "with CORAL_TELEMETRY_DISABLED a dev build sends nothing (got %v)", optNames)
}
